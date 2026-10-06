package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Profile 偏好画像(profile.json)。
// Weights 键为 "dim/value"(见 config.Key), 取值 0~1(0.5 为中立)。
// Disliked 为明显负面值的提示词片段, 合入负面提示词。
type Profile struct {
	Version   int                `json:"version"`
	Weights   map[string]float64 `json:"weights"`
	Disliked  []string           `json:"disliked"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// NewProfile 返回中性画像。
func NewProfile() *Profile {
	return &Profile{
		Version:   1,
		Weights:   map[string]float64{},
		UpdatedAt: time.Now(),
	}
}

// LoadProfile 读取画像; 不存在时返回中性画像。
func LoadProfile(dir string) (*Profile, error) {
	p := NewProfile()
	raw, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("profile.json 解析失败: %w", err)
	}
	if p.Weights == nil {
		p.Weights = map[string]float64{}
	}
	return p, nil
}

// Save 原子写入 profile.json。
func (p *Profile) Save(dir string) error {
	p.UpdatedAt = time.Now()
	return writeJSONAtomic(filepath.Join(dir, "profile.json"), p)
}

// Get 返回某维值的权重(缺省 0.5 中立)。
func (p *Profile) Get(dim, val string) float64 {
	if w, ok := p.Weights[Key(dim, val)]; ok {
		return w
	}
	return 0.5
}

// Set 设置某维值权重(裁剪到 0~1)。
func (p *Profile) Set(dim, val string, w float64) {
	if w < 0 {
		w = 0
	}
	if w > 1 {
		w = 1
	}
	p.Weights[Key(dim, val)] = w
}

// writeJSONAtomic 先写临时文件再改名, 避免半写状态。
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
