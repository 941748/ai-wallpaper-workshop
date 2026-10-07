// Package prompt 本地提示词引擎(云端 LLM 不可达时的兜底, 也是画像采样核心)。
// 规则: 每维优先从明确偏好(权重 ≥ HighPref)的值中加权采样; 无明确偏好时在活跃值
// (权重 ≥ LowCut)中加权轮换; 明显负面(低于 LowCut)的值不再采出。
// 由片段模板拼装正向提示词; 负面 = 通用负面 + 用户不喜欢片段;
// 近 30 条内禁止完全重复组合。
package prompt

import (
	"math/rand"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

// RecentWindow 去重窗口(近 30 条禁止完全重复)。
const RecentWindow = 30

// HighPref 明确偏好阈值: 达到后该维仅从高权值集合中采样(风格等维度由此稳定跟随用户选择)。
const HighPref = 0.6

// LowCut 活跃下限: 低于此权重视为明显负面(用户点踩), 不再进入采样。
const LowCut = 0.25

// HistoryEntry 一条历史记录(prompt_history.json)。
type HistoryEntry struct {
	At       time.Time         `json:"at"`
	Combo    map[string]string `json:"combo"`
	Positive string            `json:"positive"`
	Negative string            `json:"negative,omitempty"`
	Seed     int64             `json:"seed"`
	Source   string            `json:"source"` // local | cloud
}

// Spec 一轮成品的提示词与参数。
type Spec struct {
	Combo     map[string]string
	Positive  string
	Negative  string
	Seed      int64
	ShakeTurn bool // 是否"换个风格"轮
}

// Options 组合选项。
type Options struct {
	ForceShake     bool // 满意度回访"换个风格": 排除最近主风格值
	History        []HistoryEntry
	CustomKeywords []string // 用户自定义关键词(覆盖 profile 字段; 按概率融合)
}

// CustomKeywordProb 自定义关键词每轮出现概率(不每张出现, 保持新鲜感)。
const CustomKeywordProb = 0.33

// Compose 依据画像权重采样组合并拼装提示词。
func Compose(profile *config.Profile, rng *rand.Rand, opt Options) Spec {
	s := sampleCombo(profile, rng, opt)
	pos := assemble(s.Values)
	kws := opt.CustomKeywords
	if len(kws) == 0 {
		kws = profile.CustomKeywords
	}
	if kw := pickCustomKeyword(rng, kws); kw != "" {
		pos += ", " + kw
	}
	return Spec{
		Combo:     s.Values,
		Positive:  pos,
		Negative:  assembleNegative(profile),
		Seed:      s.Seed,
		ShakeTurn: opt.ForceShake,
	}
}

// pickCustomKeyword 按概率挑选一个自定义关键词融入正向提示词。
// 中文关键词直接追加, 由 Z-Image 的 Qwen 文本编码器理解(待真机验证)。
func pickCustomKeyword(rng *rand.Rand, kws []string) string {
	if len(kws) == 0 || rng.Float64() >= CustomKeywordProb {
		return ""
	}
	return kws[rng.Intn(len(kws))]
}

// sampledCombo 采样结果。
type sampledCombo struct {
	Values map[string]string
	Seed   int64
}

// sampleCombo 采样组合, 并保证近 30 条不重复。
func sampleCombo(profile *config.Profile, rng *rand.Rand, opt Options) sampledCombo {
	// "换个风格": 排除最近一条的风格主值
	exclude := map[string]string{}
	if opt.ForceShake && len(opt.History) > 0 {
		if v, ok := opt.History[len(opt.History)-1].Combo[taxonomy.DimStyle]; ok {
			exclude[taxonomy.DimStyle] = v
		}
	}
	recent := map[string]bool{}
	start := 0
	if len(opt.History) > RecentWindow {
		start = len(opt.History) - RecentWindow
	}
	for _, h := range opt.History[start:] {
		recent[comboKey(h.Combo)] = true
	}

	var values map[string]string
	for attempt := 0; attempt < 24; attempt++ {
		values = map[string]string{}
		for _, d := range taxonomy.Dimensions {
			values[d.ID] = sampleDim(profile, d, rng, exclude[d.ID])
		}
		if !recent[comboKey(values)] {
			break
		}
		// 重复: 末维顺推一步再试
		last := taxonomy.Dimensions[len(taxonomy.Dimensions)-1]
		idx := indexOf(last, values[last.ID])
		values[last.ID] = last.Values[(idx+1)%len(last.Values)].ID
		if !recent[comboKey(values)] {
			break
		}
	}
	return sampledCombo{values, seedOf(values)}
}

// sampleDim 单维采样: 明确偏好值优先(集合内按权重); 无明确偏好时在活跃值间加权轮换;
// 明显负面的值不采出; excludeVal 用于"换个风格"排除最近主值。
func sampleDim(profile *config.Profile, d taxonomy.Dimension, rng *rand.Rand, excludeVal string) string {
	type cand struct {
		id string
		w  float64
	}
	var high, active []cand
	for _, v := range d.Values {
		if excludeVal != "" && v.ID == excludeVal {
			continue
		}
		w := profile.Get(d.ID, v.ID)
		if w < LowCut {
			continue
		}
		active = append(active, cand{v.ID, w})
		if w >= HighPref {
			high = append(high, cand{v.ID, w})
		}
	}
	pool := high
	if len(pool) == 0 {
		pool = active
	}
	if len(pool) == 0 {
		// 全部被排除或均为负面: 兜底取排除值之外的第一个值。
		for _, v := range d.Values {
			if v.ID != excludeVal {
				return v.ID
			}
		}
		return d.Values[0].ID
	}
	total := 0.0
	for _, c := range pool {
		total += c.w
	}
	r := rng.Float64() * total
	for _, c := range pool {
		r -= c.w
		if r <= 0 {
			return c.id
		}
	}
	return pool[len(pool)-1].id
}

// assemble 按维度顺序拼装正向提示词。
func assemble(values map[string]string) string {
	s := ""
	for i, d := range taxonomy.Dimensions {
		v := taxonomy.ValueOf(d.ID, values[d.ID])
		if v == nil {
			continue
		}
		if i > 0 {
			s += ", "
		}
		s += v.Prompt
	}
	s += ", " + taxonomy.QualitySuffix
	return s
}

// assembleNegative 通用负面 + 用户不喜欢片段(去重拼接)。
func assembleNegative(profile *config.Profile) string {
	s := taxonomy.BaseNegative
	seen := map[string]bool{}
	for _, f := range profile.Disliked {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		s += ", " + f
	}
	return s
}

func comboKey(values map[string]string) string {
	s := ""
	for _, d := range taxonomy.Dimensions {
		s += d.ID + "=" + values[d.ID] + ";"
	}
	return s
}

func seedOf(values map[string]string) int64 {
	// 复用 probe 的确定性种子算法(fnv64a)
	h := uint64(14695981039346656037)
	for _, d := range taxonomy.Dimensions {
		for _, b := range []byte(d.ID + "=" + values[d.ID] + ";") {
			h ^= uint64(b)
			h *= 1099511628211
		}
	}
	return int64(h & 0x7fffffff)
}

func indexOf(d taxonomy.Dimension, valID string) int {
	for i, v := range d.Values {
		if v.ID == valID {
			return i
		}
	}
	return 0
}
