// Package prompt 本地提示词引擎(云端 LLM 不可达时的兜底, 也是画像采样核心)。
// 规则: 有风格包时"一图一包"——先抽 1 个已选包定五维基线, 再叠加用户微调
// (风格/题材手动喜欢并入轮换池; 色调/情绪/构图手动喜欢则锁定覆盖; 点踩值一律剔除),
// 绝不跨包拆混关键词; 无包(旧档按权重推断, 推断不出)回退逐维加权采样。
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
	Seed      int64 // 每轮随机(2026-11-10 起; 之前为组合哈希), 复现单张看 prompt_records 留存
	ShakeTurn bool  // 是否"换个风格"轮
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
	Seed   int64 // 每轮随机(见 Compose 内说明); 组合仅决定画什么
}

// InferPacks 按画像权重反推最匹配的风格包(旧档 packs 字段缺失时的迁移路径, 运行时推断不落盘)。
// 得分 = 包内高权值(≥HighPref)个数 − 包内点踩值(<LowCut)个数; 得分达包值总数一半即入选(可多选)。
func InferPacks(profile *config.Profile) []string {
	if profile == nil {
		return nil
	}
	var out []string
	for _, p := range taxonomy.PresetPacks {
		total, score := 0, 0
		for _, d := range taxonomy.Dimensions {
			for _, v := range p.Values(d.ID) {
				total++
				w := profile.Get(d.ID, v)
				if w >= HighPref {
					score++
				} else if w < LowCut {
					score--
				}
			}
		}
		if score > 0 && score*2 >= total {
			out = append(out, p.Name)
		}
	}
	return out
}

// activePacks 本画像生效的风格包: 用户显式选择优先; 旧档(字段为 nil)按权重推断。
// 显式清空(空数组)不推断, 回退逐维采样。
func activePacks(profile *config.Profile) []*taxonomy.Pack {
	var names []string
	if profile.Packs != nil {
		names = profile.Packs
	} else {
		names = InferPacks(profile)
	}
	var out []*taxonomy.Pack
	for _, n := range names {
		if p := taxonomy.PackByName(n); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// packPool 一图一包下的单维候选池:
//   - 风格/题材: 包内值 ∪ 手动喜欢的值 —— 手动勾选扩展轮换池;
//   - 色调/情绪/构图: 手动喜欢非空则锁定覆盖(仅在手动喜欢值内轮换), 否则跟随包。
//
// 权重 < LowCut(点踩)一律剔除; 包内值被点踩即从基线消失。
func packPool(profile *config.Profile, d taxonomy.Dimension, p *taxonomy.Pack) []string {
	packVals := p.Values(d.ID)
	inPack := func(id string) bool {
		for _, pv := range packVals {
			if pv == id {
				return true
			}
		}
		return false
	}
	var base, manual []string
	for _, v := range d.Values {
		w := profile.Get(d.ID, v.ID)
		if w < LowCut {
			continue
		}
		if inPack(v.ID) {
			base = append(base, v.ID)
		}
		if w >= HighPref {
			manual = append(manual, v.ID)
		}
	}
	switch d.ID {
	case taxonomy.DimPalette, taxonomy.DimMood, taxonomy.DimComposition:
		if len(manual) > 0 {
			return manual
		}
		return base
	default:
		pool := base
		seen := map[string]bool{}
		for _, id := range base {
			seen[id] = true
		}
		for _, id := range manual {
			if !seen[id] {
				pool = append(pool, id)
				seen[id] = true
			}
		}
		return pool
	}
}

// pickFromPool 在候选池中按画像权重加权采样(跳过 excludeVal 与点踩值)。
// 池空或全被排除时回退 sampleDim 逐维兜底。
func pickFromPool(profile *config.Profile, d taxonomy.Dimension, pool []string, rng *rand.Rand, excludeVal string) string {
	type cand struct {
		id string
		w  float64
	}
	var cands []cand
	for _, id := range pool {
		if id == excludeVal {
			continue
		}
		w := profile.Get(d.ID, id)
		if w < LowCut {
			continue
		}
		cands = append(cands, cand{id, w})
	}
	if len(cands) == 0 {
		return sampleDim(profile, d, rng, excludeVal)
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

// sampleCombo 采样组合, 并保证近 30 条不重复。
// 有风格包时按包整体采样(一图一包), 无包回退逐维独立采样。
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

	packs := activePacks(profile)
	var values map[string]string
	var chosen *taxonomy.Pack
	for attempt := 0; attempt < 24; attempt++ {
		values = map[string]string{}
		if len(packs) > 0 {
			chosen = packs[rng.Intn(len(packs))]
			for _, d := range taxonomy.Dimensions {
				values[d.ID] = pickFromPool(profile, d, packPool(profile, d, chosen), rng, exclude[d.ID])
			}
		} else {
			chosen = nil
			for _, d := range taxonomy.Dimensions {
				values[d.ID] = sampleDim(profile, d, rng, exclude[d.ID])
			}
		}
		if !recent[comboKey(values)] {
			break
		}
		// 重复: 末维顺推一步再试(包模式只在该包同池内顺推, 不破坏一图一包)
		last := taxonomy.Dimensions[len(taxonomy.Dimensions)-1]
		var pool []string
		if chosen != nil {
			pool = packPool(profile, last, chosen)
		}
		if len(pool) == 0 {
			for _, v := range last.Values {
				pool = append(pool, v.ID)
			}
		}
		cur := indexOfStr(pool, values[last.ID])
		values[last.ID] = pool[(cur+1)%len(pool)]
		if !recent[comboKey(values)] {
			break
		}
	}
	// seed 每轮随机(取自调用方 rng): 2026-11-10 前为组合哈希(seedOf), 会因
	// "同组合跨端撞 seed" 导致默认包用户共用构图先验(撞脸) —— 改随机后组合只决定
	// "画什么", "怎么画"每次掷骰子; 单张复现靠 prompt_records 里逐轮留存的 prompt+seed。
	return sampledCombo{values, rng.Int63()}
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

func indexOfStr(list []string, val string) int {
	for i, s := range list {
		if s == val {
			return i
		}
	}
	return -1
}
