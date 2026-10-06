// Package probe 负责探针图的平衡组合设计与点选结果 → 偏好画像推理。
package probe

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"

	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

// FirstRound / RefineRound / SurveyRound 探针轮次。
const (
	FirstRound  = "first"
	RefineRound = "refine"
	SurveyRound = "survey" // 问卷校准探针(独立 ID 前缀, 不命中云端旧公共池)
	FirstCount  = 12      // 首轮探针数
	RefineCount = 8       // 精炼轮探针数
)

// Combo 一张探针图的属性组合。
type Combo struct {
	ID     string            `json:"id"`     // p01..p12 / r01..r08
	Kind   string            `json:"kind"`   // first | refine
	Values map[string]string `json:"values"` // dimID → valueID
	Seed   int64             `json:"seed"`   // 确定性种子(公共池可复用)
}

// Key 返回组合的稳定字符串键(dim=value 按维度顺序拼接)。
func (c Combo) Key() string {
	s := ""
	for _, d := range taxonomy.Dimensions {
		s += d.ID + "=" + c.Values[d.ID] + ";"
	}
	return s
}

// Prompt 拼装该组合的正向提示词(探针图使用, 不含质量后缀以保持纯净)。
func (c Combo) Prompt() string {
	s := ""
	for i, d := range taxonomy.Dimensions {
		v := taxonomy.ValueOf(d.ID, c.Values[d.ID])
		if v == nil {
			continue
		}
		if i > 0 {
			s += ", "
		}
		s += v.Prompt
	}
	return s
}

// NameCN 组合的中文描述列表(界面提示用, 默认不展示给用户)。
func (c Combo) NameCN() []string {
	var out []string
	for _, d := range taxonomy.Dimensions {
		out = append(out, taxonomy.NameCN(d.ID, c.Values[d.ID]))
	}
	return out
}

// seedFor 由组合键确定种子(fnv64), 保证同一组合在任何端出图一致。
func seedFor(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64() & 0x7fffffff)
}

// BalancedCombos 生成 count 张 5 维尽量均衡的首轮组合(保证组合互异)。
// 方式: 每维以 (i+偏移) % n 轮转, 各值出现次数差 ≤1; 重复组合按末维顺推去重。
func BalancedCombos(count int) []Combo {
	return balancedCombos("p", FirstRound, count)
}

// SurveyCombos 问卷校准用探针组合: 与首轮同算法, 但 ID 前缀 s、轮次 SurveyRound,
// 云端公共池按 ID 取图时不会命中旧词典的存量图。
func SurveyCombos(count int) []Combo {
	return balancedCombos("s", SurveyRound, count)
}

func balancedCombos(prefix, kind string, count int) []Combo {
	combos := make([]Combo, 0, count)
	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		c := Combo{Kind: kind, Values: map[string]string{}}
		for d, dim := range taxonomy.Dimensions {
			n := len(dim.Values)
			idx := (i + d) % n
			c.Values[dim.ID] = dim.Values[idx].ID
		}
		// 去重: 若重复, 依次对末尾维度顺推一步
		for dup := 0; seen[c.Key()] && dup < 64; dup++ {
			last := taxonomy.Dimensions[len(taxonomy.Dimensions)-1]
			cur := c.Values[last.ID]
			idx := valueIndex(last, cur)
			c.Values[last.ID] = last.Values[(idx+1)%len(last.Values)].ID
		}
		seen[c.Key()] = true
		c.ID = fmt.Sprintf("%s%02d", prefix, i+1)
		c.Seed = seedFor(c.Key())
		combos = append(combos, c)
	}
	return combos
}

func valueIndex(dim taxonomy.Dimension, valID string) int {
	for i, v := range dim.Values {
		if v.ID == valID {
			return i
		}
	}
	return 0
}

// TopValues 返回画像中各维的最高权重值(平局按词典顺序)。
func TopValues(p *config.Profile) map[string]string {
	top := map[string]string{}
	for _, d := range taxonomy.Dimensions {
		best, bestW := "", -1.0
		for _, v := range d.Values {
			w := p.Get(d.ID, v.ID)
			if w > bestW {
				best, bestW = v.ID, w
			}
		}
		top[d.ID] = best
	}
	return top
}

// RefineCombos 围绕画像高分组合生成 count 张精炼组合(可整轮跳过)。
// 方式: 以各维 top 值为基准, 每张图在 2 个维度上换为次优值, 保证组合互异。
func RefineCombos(p *config.Profile, count int) []Combo {
	top := TopValues(p)
	// 每维按权重降序排列(排除 top 后的次序即"次优"顺序)
	ranked := map[string][]string{}
	for _, d := range taxonomy.Dimensions {
		ids := make([]string, 0, len(d.Values))
		for _, v := range d.Values {
			ids = append(ids, v.ID)
		}
		sort.SliceStable(ids, func(a, b int) bool {
			return p.Get(d.ID, ids[a]) > p.Get(d.ID, ids[b])
		})
		ranked[d.ID] = ids
	}

	combos := make([]Combo, 0, count)
	seen := map[string]bool{}
	steps := 1
	for len(combos) < count && steps < 8 {
		for k := 0; k < len(taxonomy.Dimensions) && len(combos) < count; k++ {
			c := Combo{Kind: RefineRound, Values: map[string]string{}}
			for _, d := range taxonomy.Dimensions {
				c.Values[d.ID] = top[d.ID]
			}
			// 两个维度换成第 sec 顺位值
			d1 := taxonomy.Dimensions[k]
			d2 := taxonomy.Dimensions[(k+1)%len(taxonomy.Dimensions)]
			c.Values[d1.ID] = nthRanked(ranked[d1.ID], steps)
			c.Values[d2.ID] = nthRanked(ranked[d2.ID], steps)
			if seen[c.Key()] {
				continue
			}
			seen[c.Key()] = true
			c.ID = fmt.Sprintf("r%02d", len(combos)+1)
			c.Seed = seedFor(c.Key())
			combos = append(combos, c)
		}
		steps++
	}
	return combos
}

func nthRanked(ids []string, n int) string {
	if n >= len(ids) {
		n = len(ids) - 1
	}
	return ids[n]
}

// Choice 用户对一张探针图的选择。
type Choice int

// 点选状态: 中立 → 喜欢 → 不喜欢 循环。
const (
	Neutral Choice = iota
	Like
	Dislike
)

// Infer 由点选结果推理画像。
// 每值得分 = (喜欢数 - 不喜欢数) / (出现次数), 映射到 0~1(0.5 中立);
// 若有 prev 画像则按 0.65/0.35 融合(重配置往往只是微调);
// 得分显著为负的值片段合入 Disliked。
func Infer(combos []Combo, choices map[string]Choice, prev *config.Profile) *config.Profile {
	type stat struct{ pos, neg, total int }
	stats := map[string]*stat{}
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			stats[config.Key(d.ID, v.ID)] = &stat{}
		}
	}
	for _, c := range combos {
		ch, ok := choices[c.ID]
		if !ok {
			ch = Neutral
		}
		for dimID, valID := range c.Values {
			st := stats[config.Key(dimID, valID)]
			st.total++
			switch ch {
			case Like:
				st.pos++
			case Dislike:
				st.neg++
			}
		}
	}

	next := config.NewProfile()
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			st := stats[config.Key(d.ID, v.ID)]
			score := 0.0
			if st.total > 0 {
				score = float64(st.pos-st.neg) / float64(st.total)
			}
			w := 0.5 + score*0.5
			if prev != nil {
				w = prev.Get(d.ID, v.ID)*0.35 + w*0.65
			}
			next.Set(d.ID, v.ID, w)
			// 明显负面: 新证据下净负面且出现至少 1 次
			if st.neg > st.pos && st.neg > 0 {
				if f := v.Negative; f != "" {
					next.Disliked = append(next.Disliked, f)
				} else {
					next.Disliked = append(next.Disliked, v.Prompt)
				}
			}
		}
	}
	if prev != nil && next.Version <= prev.Version {
		next.Version = prev.Version + 1
	}
	return next
}

// Summarize 返回画像的中文摘要(界面确认页展示, 如 "冷蓝青 0.90 · 壮阔 0.71")。
func Summarize(p *config.Profile, topN int) []string {
	type kv struct {
		name string
		w    float64
	}
	var out []kv
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			w := p.Get(d.ID, v.ID)
			if w > 0.55 {
				out = append(out, kv{taxonomy.NameCN(d.ID, v.ID), w})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].w > out[b].w })
	if len(out) > topN {
		out = out[:topN]
	}
	res := make([]string, 0, len(out))
	for _, e := range out {
		res = append(res, fmt.Sprintf("%s %.2f", e.name, e.w))
	}
	return res
}

// WeightedDistance 组合间加权差异(0~1, 用于判断"是否明显不同的风格")。
func WeightedDistance(a, b map[string]string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 1
	}
	diff, total := 0, 0
	for dimID, va := range a {
		total++
		if vb := b[dimID]; vb != va {
			diff++
		}
	}
	return math.Round(float64(diff)/float64(total)*100) / 100
}
