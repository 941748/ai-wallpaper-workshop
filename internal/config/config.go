// Package config 负责客户端配置与偏好画像的读写。
// 数据目录默认 %LOCALAPPDATA%\AIWallpaper, 主要文件:
//
//	config.json         主配置(云端地址/配额相位/安静时段/满意度周期)
//	profile.json        偏好画像(5 维权重 + 负面片段)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 关键默认值
const (
	// DefaultCloudURL 默认云端服务地址: 开源版留空, 由用户在向导中填写(或勾选直连模式);
	// 自部署后可改为自己的固定域名。
	DefaultCloudURL     = ""
	DefaultIntervalHour = 1
	DefaultAskDays      = 3 // 满意度回访默认周期(天)
	TaskName            = "AIWallpaper"
	AppNameCN           = "AI 壁纸工坊"
)

// AskLadder 回访记忆曲线阶梯(天)。
// 设计目的: "尽量少打扰"但不让用户遗忘产品存在感——唤醒节律前密后疏, 90 天封顶(每季度一次的底线节律)。
var AskLadder = []int{3, 7, 15, 30, 60, 90}

// Satisfaction 满意度回访状态(存 config.json)。
// 记忆曲线: 首次 3 天; 满意/无响应 → 沿阶梯上调一档(3→7→15→30→60→90 封顶);
// 换个风格 → 重置 3 天并以强漂移事件上报;
// 以后再说/调整一下 → NextAskAt 顺延 3 天(档位不变)。
type Satisfaction struct {
	IntervalDays int    `json:"interval_days"`
	NextAskAt    string `json:"next_ask_at"` // RFC3339, 空=尚未初始化
	LastChoice   string `json:"last_choice"` // satisfied | style | later | adjust | timeout
}

// Config 客户端主配置。
type Config struct {
	SchemaVersion int  `json:"schema_version"`
	Initialized   bool `json:"initialized"`

	// 云端身份(匿名, 零注册零登录)
	UserID   string `json:"user_id"`
	Token    string `json:"token"`
	CloudURL string `json:"cloud_url"`

	// 直连调试模式(仅局域网联调, 默认关闭)
	DirectMode  bool   `json:"direct_mode"`
	DirectURL   string `json:"direct_url"`
	DirectToken string `json:"direct_token"`

	// 调度
	IntervalHours int `json:"interval_hours"`
	PhaseMinutes  int `json:"phase_minutes"` // 0~59 换图相位(按 user_id hash 分散)

	// 节日/节气语境(画面氛围轻推; 默认开启, 用户可关)
	DisableContext bool `json:"disable_context,omitempty"`

	// 安静模式(前台全屏/静默时段不打扰)
	QuietEnabled bool   `json:"quiet_enabled"`
	QuietStart   string `json:"quiet_start"` // HH:MM
	QuietEnd     string `json:"quiet_end"`   // HH:MM

	// 画像版本(重配置后 +1, 云端预生成据此作废)
	ProfileVer int `json:"profile_version"`

	Satisfaction Satisfaction `json:"satisfaction"`
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		SchemaVersion: 1,
		CloudURL:      DefaultCloudURL,
		IntervalHours: DefaultIntervalHour,
		QuietEnabled:  true,
		QuietStart:    "23:00",
		QuietEnd:      "07:00",
		Satisfaction: Satisfaction{
			IntervalDays: DefaultAskDays,
		},
	}
}

// DefaultDir 返回数据目录 %LOCALAPPDATA%\AIWallpaper。
func DefaultDir() string {
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			la = filepath.Join(home, "AppData", "Local")
		} else {
			la = "."
		}
	}
	return filepath.Join(la, "AIWallpaper")
}

// EnsureSubdirs 创建数据子目录(probes/wallpapers/bin/logs)。
func EnsureSubdirs(dir string) error {
	for _, sub := range []string{"probes", "wallpapers", "bin", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Load 读取配置; 文件不存在时返回默认配置。
func Load(dir string) (*Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config.json 解析失败: %w", err)
	}
	if cfg.IntervalHours <= 0 {
		cfg.IntervalHours = DefaultIntervalHour
	}
	if cfg.Satisfaction.IntervalDays <= 0 {
		cfg.Satisfaction.IntervalDays = DefaultAskDays
	}
	if cfg.PhaseMinutes < 0 || cfg.PhaseMinutes > 59 {
		cfg.PhaseMinutes = 0
	}
	return cfg, nil
}

// Save 原子写入 config.json。
func (c *Config) Save(dir string) error {
	return writeJSONAtomic(filepath.Join(dir, "config.json"), c)
}

// NextAskTime 返回下次回访时间; 未设置时返回零值。
func (s *Satisfaction) NextAskTime() time.Time {
	if s.NextAskAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s.NextAskAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ScheduleAsk 基于 now 按记忆曲线安排下一次回访:
// 满意/无响应 → 上调一档(封顶 90 天, 手动设置的超长周期不被缩短);
// 换个风格 → 重置首档; 以后再说/调整一下 → 档位不变仅顺延 3 天。
func (s *Satisfaction) ScheduleAsk(now time.Time, choice string) {
	if s.IntervalDays <= 0 {
		s.IntervalDays = AskLadder[0]
	}
	stage := askStage(s.IntervalDays)
	switch choice {
	case "satisfied", "timeout":
		if stage < len(AskLadder)-1 {
			stage++
		}
		if next := AskLadder[stage]; next > s.IntervalDays {
			s.IntervalDays = next
		}
	case "style":
		s.IntervalDays = AskLadder[0] // 换了风格, 重新密集观察
	case "later", "adjust":
		// 档位不变, 仅顺延 3 天
	default: // 首次初始化
		s.IntervalDays = AskLadder[askStage(s.IntervalDays)]
	}
	s.LastChoice = choice
	days := s.IntervalDays
	if choice == "later" || choice == "adjust" {
		days = 3
	}
	s.NextAskAt = now.AddDate(0, 0, days).Format(time.RFC3339)
}

// askStage 按当前周期推导曲线档位(取不超过 IntervalDays 的最高档, 兼容手动与旧数据)。
func askStage(days int) int {
	stage := 0
	for i, d := range AskLadder {
		if days >= d {
			stage = i
		}
	}
	return stage
}

// 画像权重维值 Key 形如 "style/ink"。
func Key(dim, val string) string { return dim + "/" + val }
