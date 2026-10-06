// Package signals 漂移事件采集(本地待上报队列)。
// 漂移依据全部来自用户自然操作: 重跑向导/重新点选、历史重应用、调权重、
// 满意度回访选择(style_keep/style_reject)。事件先本地落盘, 随下一轮上报云端。
package signals

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// 事件类型。
const (
	TypeReconfigure    = "reconfigure"     // 重跑向导/重新点选探针(最强信号)
	TypeReapply        = "reapply"         // 历史页重新应用某张壁纸
	TypeWeightAdjust   = "weight_adjust"   // 设置页手工微调权重
	TypeKeywordsAdjust = "keywords_adjust" // 设置页自定义关键词增删(强偏好方向信号)
	TypeStyleKeep      = "style_keep"      // 满意度回访: 满意(弱正向)
	TypeStyleReject    = "style_reject"    // 满意度回访: 换个风格(强拒斥)
	TypeLiked          = "liked"           // 设置页当前壁纸: 喜欢(强正向, Extra 带 combo)
	TypeDisliked       = "disliked"        // 设置页当前壁纸: 不喜欢(强拒斥, Extra 带 combo)
	TypeDeviceLinked   = "device_linked"   // 新设备扫码配对成功
	TypeReinitialized  = "reinitialized"   // 整轮重新初始化完成
)

// Event 一条漂移事件。
type Event struct {
	ID     string         `json:"id"`
	At     time.Time      `json:"at"`
	Type   string         `json:"type"`
	Detail string         `json:"detail,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"` // 如重新应用的壁纸组合
}

const fileName = "signals.json"

// Load 读取待上报事件。
func Load(dir string) ([]Event, error) {
	var evs []Event
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return evs, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &evs); err != nil {
		return nil, err
	}
	return evs, nil
}

// Append 追加一条事件(自动补 ID/时间)。
func Append(dir string, ev Event) error {
	if ev.ID == "" {
		ev.ID = newID()
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	evs, err := Load(dir)
	if err != nil {
		evs = nil
	}
	evs = append(evs, ev)
	return save(dir, evs)
}

// Clear 清空待上报事件(上报成功后调用)。
func Clear(dir string) error { return save(dir, []Event{}) }

func save(dir string, evs []Event) error {
	if evs == nil {
		evs = []Event{}
	}
	data, err := json.MarshalIndent(evs, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
