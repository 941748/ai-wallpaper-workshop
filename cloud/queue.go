package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"wallpaper/internal/probe"

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
	lastPool  time.Time     // 上次探针池检查
	idleDelay time.Duration // 默认 20s; 测试可调小
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
	// 空闲时段定期检查探针池(缺图/失败重排; 低开销, 每分钟至多一次)
	if time.Since(q.lastPool) > time.Minute {
		q.lastPool = time.Now()
		q.ensureProbePool()
	}
	// 空闲才跑 idle(预生成/probe 池)
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
	img, err := q.comfy.Generate(ctx2, job.Positive, job.Width, job.Height, job.Seed)
	if err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		q.logf("任务失败 job=%s kind=%s: %v", job.JobID, job.Kind, err)
		return
	}

	var path string
	switch job.Kind {
	case "probe":
		// 公共探针池: 存到 probes/<id>.png
		path = filepath.Join(q.dataDir, "probes", job.Combo+".png")
	case "pregen":
		path = filepath.Join(q.dataDir, "pregen", job.JobID+".png")
	default:
		path = filepath.Join(q.dataDir, "images", job.JobID+".png")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, img, 0o644); err != nil {
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
	}
	q.logf("任务完成 job=%s kind=%s -> %s", job.JobID, job.Kind, path)
}

// ensureProbePool 维护公共探针池: 缺哪张补哪张(低优先级, 空闲时段执行)。
// 与服务端/客户端共用的 12 张平衡组合, 新用户可秒开。
func (q *queue) ensureProbePool() {
	combos := probe.BalancedCombos(probe.FirstCount)
	for _, cb := range combos {
		path := filepath.Join(q.dataDir, "probes", cb.ID+".png")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		// 幂等任务 id(已存在则不重复建); 上次失败(如出图机离线)则重新排队
		jobID := "probe-" + cb.ID
		if existing, _ := q.st.Job(jobID); existing != nil {
			if existing.Status == "failed" {
				_ = q.st.SetJobStatus(jobID, "queued", "", "")
			}
			continue
		}
		_ = q.st.CreateJob(store.Job{
			JobID:      jobID,
			UserID:     "system",
			Kind:       "probe",
			Priority:   "idle",
			Positive:   cb.Prompt(),
			Negative:   "lowres, blurry, watermark, text",
			Combo:      cb.ID,
			Width:      1024,
			Height:     768,
			Seed:       cb.Seed,
			WorkflowID: q.reg.Default().ID,
		})
	}
}
