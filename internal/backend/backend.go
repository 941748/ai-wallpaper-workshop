// Package backend 定义出图后端抽象。
// 两个实现: cloud(默认, 经云端 /generate 代理)与 comfyui-direct(调试, 直连 ComfyUI)。
package backend

import "context"

// GenParams 一轮出图参数(宽度/高度由云端 LLM 在约束内确定后传入)。
type GenParams struct {
	Positive   string  `json:"positive"`
	Negative   string  `json:"negative"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Seed       int64   `json:"seed"`
	Steps      int     `json:"steps,omitempty"` // 0 = 工作流默认
	CFG        float64 `json:"cfg,omitempty"`
	WorkflowID string  `json:"workflow_id,omitempty"` // 云端工作流白名单 ID(直连调试可空)
	Priority   string  `json:"priority,omitempty"`    // normal | idle(空闲预生成)
}

// JobID 任务标识。
type JobID string

// ImageBackend 出图后端接口。
// Submit 提交任务返回 JobID; Wait 等待完成并返回成品图字节(PNG)。
type ImageBackend interface {
	Submit(ctx context.Context, p GenParams) (JobID, error)
	Wait(ctx context.Context, id JobID) ([]byte, error)
}
