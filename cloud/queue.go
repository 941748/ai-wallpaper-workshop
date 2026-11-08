package main

import (
	"context"
	"time"

	"wallpaper/cloud/store"
)

// queue 云端协调器(S3): 出图机代理(aw-agent)领任务渲染, 云端不再直连 ComfyUI、
// 不再有隧道兜底——代理是唯一渲染链路, 因此保留两件事保命:
// ①空闲周期触发主动预生成钩子; ②代理卡死回收(认领超时 → 明确失败, 客户端可续传重试)。
type queue struct {
	st       *store.Store
	logf     func(string, ...any)
	lastIdle time.Time
	idleHook func() // 空闲周期钩子(主动预生成调度器), 可为 nil
}

// Run 工作循环。
func (q *queue) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
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
	// 代理卡死回收: 认领后 10 分钟未回报(进程死亡/断网) → 明确失败
	if n, err := q.st.FailStaleRunning(time.Now().Add(-10 * time.Minute)); err != nil {
		q.logf("卡死任务回收失败: %v", err)
	} else if n > 0 {
		q.logf("代理未回报, %d 个任务标记失败(出图机离线或渲染超时)", n)
		if m, err := q.st.DropPregensForFailed(); err == nil && m > 0 {
			q.logf("清理失败任务残留预生成行: %d", m)
		}
	}
	// 空闲周期主动预生成(为近24h活跃用户保持 ready 库存; 每分钟至多一次)
	if time.Since(q.lastIdle) > time.Minute {
		q.lastIdle = time.Now()
		if q.idleHook != nil {
			q.idleHook()
		}
	}
}
