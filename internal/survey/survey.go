// Package survey 偏好问卷: 五维三态(喜欢/中立/讨厌)点选 → 画像权重映射。
// 映射契约: 喜欢=0.9 / 中立=0.3 / 讨厌=0.05; 所有词典值均显式写入权重,
// 讨厌项的避让片段(值自身描述)合入画像 Disliked(去重追加)。
package survey

import (
	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

// Attitude 用户对某偏好项的态度(与界面三态复选框状态一一对应)。
type Attitude int

// 三态取值。
const (
	Neutral Attitude = iota // 中立(未勾选)
	Like                    // 喜欢
	Dislike                 // 讨厌
)

// 三态对应的画像权重。
const (
	LikeWeight    = 0.9
	NeutralWeight = 0.3
	DislikeWeight = 0.05
)

// Apply 把问卷答案映射为画像权重并合入 p(就地修改并返回)。
// 答案键为 config.Key(dim,val); 答过的项按三态取值, 未答的项按中立 0.3;
// 未知键忽略; 画像版本 +1。
func Apply(p *config.Profile, answers map[string]Attitude) *config.Profile {
	if p == nil {
		p = config.NewProfile()
	}
	p.Version++
	if p.Weights == nil {
		p.Weights = map[string]float64{}
	}
	disliked := map[string]bool{}
	for _, f := range p.Disliked {
		disliked[f] = true
	}
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			switch answers[config.Key(d.ID, v.ID)] {
			case Like:
				p.Set(d.ID, v.ID, LikeWeight)
			case Dislike:
				p.Set(d.ID, v.ID, DislikeWeight)
				// 避让词用值自身的描述; 不可用 v.Negative —— 负向词语义相反,
				// 曾把"讨厌二次元"错误转译成"避开照片写实"。
				frag := v.Prompt
				if frag != "" && !disliked[frag] {
					p.Disliked = append(p.Disliked, frag)
					disliked[frag] = true
				}
			default:
				p.Set(d.ID, v.ID, NeutralWeight)
			}
		}
	}
	return p
}
