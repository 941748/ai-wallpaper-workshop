package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码(ComfyUI 出图为 PNG)
	"os"
	"path/filepath"
	"time"

	"wallpaper/cloud/store"
)

// queue 出图任务队列: normal 优先; idle 仅在普通队列空闲后执行(空闲预生成)。
type queue struct {
	st        *store.Store
	comfy     *Comfy
	reg       *Registry
	dataDir   string
	logf      func(string, ...any)
	lastNorm  time.Time
	lastIdle  time.Time     // 上次空闲钩子(主动预生成)执行
	idleDelay time.Duration // 默认 20s; 测试可调小
	idleHook  func()        // 空闲周期钩子(主动预生成调度器), 可为 nil
}

// Run 工作循环(单 worker, 顺序消化 4090)。
func (q *queue) Run(ctx context.Context) {
	q.lastNorm = time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		q.tick(ctx)
	}
}

func (q *queue) tick(ctx context.Context) {
	// OSS 直传兜底: 代理超时未回报的任务回退隧道拉图
	if ossUploadEnabled() {
		q.reclaim(ctx)
	}
	// 优先 normal
	job, err := q.st.NextQueuedJob(false)
	if err != nil {
		q.logf("队列查询失败: %v", err)
		return
	}
	if job != nil {
		q.lastNorm = time.Now()
		q.process(ctx, job)
		return
	}
	// 空闲时段周期性执行钩子(主动预生成调度; 低开销, 每分钟至多一次)
	if time.Since(q.lastIdle) > time.Minute {
		q.lastIdle = time.Now()
		if q.idleHook != nil {
			q.idleHook() // 主动预生成: 为活跃用户保持 ready 库存
		}
	}
	// 空闲才跑 idle(预生成)
	delay := q.idleDelay
	if delay <= 0 {
		delay = 20 * time.Second
	}
	if time.Since(q.lastNorm) < delay {
		return
	}
	job, err = q.st.NextQueuedJob(true)
	if err != nil || job == nil {
		return
	}
	q.process(ctx, job)
}

func (q *queue) process(ctx context.Context, job *store.Job) {
	_ = q.st.SetJobStatus(job.JobID, "running", "", "")
	ctx2, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()

	// P1 OSS 直传(AW_OSS_UPLOAD=1): 只提交+等完成, 字节由出图机本地代理直传 OSS;
	// 30s 未回报由 reclaim 兜底拉图。开关关闭 = 现网一体 Generate 行为(零变化)。
	if ossUploadEnabled() {
		pid, err := q.comfy.Submit(ctx2, job.Positive, job.Width, job.Height, job.Seed)
		if err == nil {
			var ref imageRef
			ref, err = q.comfy.WaitDone(ctx2, pid)
			if err == nil {
				if rerr := q.st.SetJobRender(job.JobID, pid, ref.Filename, ref.Subfolder); rerr != nil {
					q.logf("记录渲染引用失败 job=%s: %v", job.JobID, rerr)
					return
				}
				q.logf("任务渲染完成,等待代理上传 job=%s kind=%s file=%s", job.JobID, job.Kind, ref.Filename)
				return
			}
		}
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		q.logf("任务失败 job=%s kind=%s: %v", job.JobID, job.Kind, err)
		return
	}

	img, err := q.comfy.Generate(ctx2, job.Positive, job.Width, job.Height, job.Seed)
	if err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		q.logf("任务失败 job=%s kind=%s: %v", job.JobID, job.Kind, err)
		return
	}
	q.finish(job, img)
}

// finish 落盘收尾: 转码 JPEG → 写文件 → done(pregen 同步置 ready)。直传兜底路径复用。
func (q *queue) finish(job *store.Job, img []byte) {
	// 落盘前转码 JPEG: 云端→客户端下载体积缩至 PNG 的约 1/4~1/5(带宽瓶颈在这一段)。
	// 失败时保留原 PNG, 由后缀决定下发 MIME。
	data := img
	ext := ".png"
	if jpg, ok := toJPEG(img); ok {
		data, ext = jpg, ".jpg"
	}

	var path string
	switch job.Kind {
	case "pregen":
		path = filepath.Join(q.dataDir, "pregen", job.JobID+ext)
	default:
		path = filepath.Join(q.dataDir, "images", job.JobID+ext)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		return
	}
	_ = q.st.SetJobStatus(job.JobID, "done", "", path)

	if job.Kind == "pregen" {
		_ = q.st.SetPregenReady(job.JobID, path)
	} else if err := q.st.ArchiveJob(job.JobID); err != nil {
		q.logf("归档失败 job=%s: %v", job.JobID, err)
	}
	q.logf("任务完成 job=%s kind=%s -> %s", job.JobID, job.Kind, path)
}

// reclaim OSS 直传兜底: oss_pending 超 30s 未获代理确认 → 回退隧道拉图落盘(现网路径)。
// 图仍留在 Comfy(未删), 失败保持 oss_pending 下轮再试。
func (q *queue) reclaim(ctx context.Context) {
	q.reclaimBefore(ctx, time.Now().Add(-30*time.Second))
}

// reclaimBefore 按截止时间回收(时间参数化供测试)。
func (q *queue) reclaimBefore(ctx context.Context, cutoff time.Time) {
	tasks, err := q.st.ListStaleOssPending(cutoff)
	if err != nil || len(tasks) == 0 {
		return
	}
	for _, t := range tasks {
		q.logf("代理未回报, 兜底拉图 job=%s", t.JobID)
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		ref, rerr := q.comfy.WaitDone(cctx, t.PromptID) // 已完成 → 立即返回
		if rerr == nil {
			var img []byte
			img, rerr = q.comfy.FetchView(cctx, ref)
			if rerr == nil {
				q.finish(&store.Job{JobID: t.JobID, Kind: t.Kind}, img)
				cancel()
				continue
			}
		}
		q.logf("兜底拉图失败 job=%s: %v", t.JobID, rerr)
		cancel()
	}
}

// jpegQuality 下发成品的 JPEG 质量(体积与画质折中; 客户端设壁纸前还会转码, 此处无二次损失)。
const jpegQuality = 90

// toJPEG 把成品图转码为 JPEG 以压缩下发体积; 解码失败返回 ok=false(保留原图)。
func toJPEG(src []byte) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}
