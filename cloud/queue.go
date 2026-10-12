package main

import (
	"context"
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
	img, err := q.comfy.Generate(ctx2, job.Positive, job.Width, job.Height, job.Seed)
	if err != nil {
		_ = q.st.SetJobStatus(job.JobID, "failed", err.Error(), "")
		q.logf("任务失败 job=%s kind=%s: %v", job.JobID, job.Kind, err)
		return
	}

	var path string
	switch job.Kind {
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
