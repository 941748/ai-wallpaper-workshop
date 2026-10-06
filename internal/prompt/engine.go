// Package prompt 本地提示词引擎(云端 LLM 不可达时的兜底, 也是画像采样核心)。
// 规则: 每维按权重采样一个值(权重下限 0.05 保证多样性; 15% 概率探索非最高值),
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

// ExploreProb 每维探索概率(选非最高值)。
const ExploreProb = 0.15

// WeightFloor 采样权重下限。
const WeightFloor = 0.05

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

// sampleDim 单维加权采样。
func sampleDim(profile *config.Profile, d taxonomy.Dimension, rng *rand.Rand, excludeVal string) string {
	type cand struct {
		id string
		w  float64
	}
	var cands []cand
	for _, v := range d.Values {
		if excludeVal != "" && v.ID == excludeVal {
			continue
		}
		w := profile.Get(d.ID, v.ID)
		if w < WeightFloor {
			w = WeightFloor
		}
		cands = append(cands, cand{v.ID, w})
	}
	if len(cands) == 0 {
		return d.Values[0].ID
	}
	// 探索: 以 ExploreProb 概率排除当前最高值后再采样
	if len(cands) > 1 && rng.Float64() < ExploreProb {
		top := 0
		for i := range cands {
			if cands[i].w > cands[top].w {
				top = i
			}
		}
		cands = append(cands[:top], cands[top+1:]...)
	}
	total := 0.0
	for _, c := range cands {
		total += c.w
	}
	r := rng.Float64() * total
	for _, c := range cands {
		r -= c.w
		if r <= 0 {
			return c.id
		}
	}
	return cands[len(cands)-1].id
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
