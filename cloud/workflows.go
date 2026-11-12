package main

import (
	"encoding/json"
	"os"
	"sort"
)

// Lora 工作流中的 LoRA。
type Lora struct {
	Name          string  `json:"name"`
	StrengthModel float64 `json:"strength_model"`
	StrengthClip  float64 `json:"strength_clip"`
}

// Workflow 一套出图工作流。
// 出图工作流已固定为 Z-Image Turbo(z_image_wallpaper), 模型与采样参数冻结在模板内;
// 注册表仅保留工作流白名单, 其余字段仅作备注。
// P5: 渲染限宽已移交出图机配置(aw-agent 的 max_side, 8GB 显存=2048, 更大显存放开直出物理),
// 服务端 min_side/max_side 不再参与决策(配置文件里的同名键会被忽略)。
type Workflow struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Checkpoint  string  `json:"checkpoint"` // 备注: 模板内已固定 UNET/CLIP/VAE, 不再注入
	Loras       []Lora  `json:"loras,omitempty"`
	Steps       int     `json:"steps"` // 备注: 模板内已固定, 不再注入
	CFG         float64 `json:"cfg"`
	SamplerName string  `json:"sampler_name"`
	Scheduler   string  `json:"scheduler"`
}

// Registry 工作流注册表(白名单: LLM 只能在其中选择)。
type Registry struct {
	workflows map[string]Workflow
}

// DefaultRegistry 内置默认注册表(仅基础工作流;
// 增加尺寸档位后在 data/workflows.json 中追加即可, 客户端无感)。
func DefaultRegistry() *Registry {
	r := &Registry{workflows: map[string]Workflow{}}
	r.add(Workflow{
		ID: "wf-base", Name: "Z-Image Turbo 通用",
		Checkpoint: "z_image_turbo_int8_convrot.safetensors",
		Steps:      8, CFG: 1.0,
		SamplerName: "res_multistep", Scheduler: "simple",
	})
	return r
}

func (r *Registry) add(w Workflow) {
	if w.Steps <= 0 {
		w.Steps = 8
	}
	if w.CFG <= 0 {
		w.CFG = 1
	}
	if w.SamplerName == "" {
		w.SamplerName = "res_multistep"
	}
	if w.Scheduler == "" {
		w.Scheduler = "simple"
	}
	r.workflows[w.ID] = w
}

// LoadRegistry 读取注册表文件; 不存在时用默认并写回示例。
func LoadRegistry(path string) *Registry {
	r := DefaultRegistry()
	raw, err := os.ReadFile(path)
	if err != nil {
		// 生成示例文件便于管理员编辑
		list := r.List()
		if data, err := json.MarshalIndent(list, "", "  "); err == nil {
			_ = os.WriteFile(path, data, 0o644)
		}
		return r
	}
	var list []Workflow
	if err := json.Unmarshal(raw, &list); err != nil {
		return r
	}
	r = &Registry{workflows: map[string]Workflow{}}
	for _, w := range list {
		if w.ID != "" && w.Checkpoint != "" {
			r.add(w)
		}
	}
	if len(r.workflows) == 0 {
		return DefaultRegistry()
	}
	return r
}

// Get 按 ID 取工作流。
func (r *Registry) Get(id string) (Workflow, bool) {
	w, ok := r.workflows[id]
	return w, ok
}

// Default 默认工作流(未知 ID 的回退)。
func (r *Registry) Default() Workflow {
	if w, ok := r.workflows["wf-base"]; ok {
		return w
	}
	for _, w := range r.workflows {
		return w
	}
	return Workflow{ID: "wf-base", Checkpoint: "z_image_turbo_int8_convrot.safetensors", Steps: 8, CFG: 1}
}

// Resolve 白名单校验: 非法 ID 回退默认。
func (r *Registry) Resolve(id string) Workflow {
	if w, ok := r.workflows[id]; ok {
		return w
	}
	return r.Default()
}

// IDs 全部工作流 ID(提示词里告知 LLM)。
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.workflows))
	for id := range r.workflows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// List 全部工作流(管理/示例文件用)。
func (r *Registry) List() []Workflow {
	out := make([]Workflow, 0, len(r.workflows))
	for _, w := range r.workflows {
		out = append(out, w)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}
