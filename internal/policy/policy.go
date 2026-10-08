// Package policy 运营策略模型: 换图活跃时段 / 本地备用池目标 / 暂停选项等。
// 策略由服务端 policy.json 下发(接口 GET /api/v1/policy), 客户端每轮 tick 同步缓存;
// 断网或未配置时使用内置默认, 保证行为始终合理。策略参数对用户不可见。
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Policy 策略参数(全部为运营侧可调项)。
type Policy struct {
	Version      int         `json:"policy_version"`     // 服务端递增; 仅用于观察与比较
	ActiveBlocks [][2]string `json:"active_blocks"`      // 换图活跃时段 [["09:00","12:00"], ...]
	PoolTarget   int         `json:"pool_target"`        // 本地备用池目标张数
	PauseOptions []int       `json:"pause_options_hours"` // 暂停换图可选时长(小时)
	MaxPause     int         `json:"max_pause_hours"`     // 暂停兜底上限(小时)
}

// Default 内置默认策略(服务端未下发时的行为基准)。
func Default() Policy {
	return Policy{
		Version:      0,
		ActiveBlocks: [][2]string{{"09:00", "12:00"}, {"14:00", "18:00"}, {"20:00", "24:00"}},
		PoolTarget:   3,
		PauseOptions: []int{8, 24, 48},
		MaxPause:     48,
	}
}

// Normalize 修复缺失/越界字段(读服务端响应或本地缓存后调用)。
func (p Policy) Normalize() Policy {
	d := Default()
	if len(p.ActiveBlocks) == 0 {
		p.ActiveBlocks = d.ActiveBlocks
	}
	if p.PoolTarget < 1 || p.PoolTarget > 10 {
		p.PoolTarget = d.PoolTarget
	}
	var opts []int
	for _, h := range p.PauseOptions {
		if h >= 1 && h <= 24*30 {
			opts = append(opts, h)
		}
	}
	if len(opts) == 0 {
		opts = d.PauseOptions
	}
	p.PauseOptions = opts
	if p.MaxPause < 1 {
		p.MaxPause = d.MaxPause
	}
	return p
}

// InActive 判断 now 是否落在任一活跃时段(左闭右开; "24:00" 表示当日终)。
func (p Policy) InActive(now time.Time) bool {
	cur := now.Hour()*60 + now.Minute()
	for _, b := range p.ActiveBlocks {
		s, ok1 := parseHHMM(b[0])
		e, ok2 := parseHHMM(b[1])
		if !ok1 || !ok2 || s >= e {
			continue
		}
		if cur >= s && cur < e {
			return true
		}
	}
	return false
}

// ActiveDesc 活跃时段描述(日志用), 形如 "09:00-12:00,14:00-18:00,20:00-24:00"。
func (p Policy) ActiveDesc() string {
	out := ""
	for i, b := range p.ActiveBlocks {
		if i > 0 {
			out += ","
		}
		out += b[0] + "-" + b[1]
	}
	return out
}

// PauseLabel 暂停时长的人类描述(界面用)。
func PauseLabel(hours int) string {
	switch {
	case hours%24 == 0 && hours >= 24:
		return fmt.Sprintf("%d 天", hours/24)
	case hours > 24:
		return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
	default:
		return fmt.Sprintf("%d 小时", hours)
	}
}

func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' ||
		s[3] < '0' || s[3] > '9' || s[4] < '0' || s[4] > '9' {
		return 0, false
	}
	if h == 24 && m == 0 {
		return 24 * 60, true // "24:00" = 当日终
	}
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Load 读取本地策略缓存(<dir>/policy.json); 不存在或非法时返回默认。
func Load(dir string) Policy {
	raw, err := os.ReadFile(filepath.Join(dir, "policy.json"))
	if err != nil {
		return Default()
	}
	var p Policy
	if json.Unmarshal(raw, &p) != nil {
		return Default()
	}
	return p.Normalize()
}

// Save 写入本地策略缓存。
func Save(dir string, p Policy) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "policy.json"), raw, 0o644)
}
