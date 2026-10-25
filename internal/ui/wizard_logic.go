package ui

// 向导的纯决策逻辑(不依赖 walk 窗口): 问卷三态换算、风格包预设、勾选统计与摘要、
// 完成页回显、相位分配等。抽离至此便于单元测试(见 wizard_test.go), 并让 wizard.go
// 专注于 UI 构建与流程编排。

import (
	"hash/fnv"
	"strings"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/prompt"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// firstWallpaperTimeout 首张壁纸现场出图上限: 单张典型 30-45s, 留排队/慢卡余量到 90s; 超时即跳过稍后补。
const firstWallpaperTimeout = 90 * time.Second

// msgRenderBusy 出图链路不可用(出图机离线/繁忙/超时)时对用户的统一文案; 不暴露技术原因。
const msgRenderBusy = "系统繁忙, 初始化完成后会自动无感补图"

// attitudeOfWeight 按画像权重反推问卷三态(与落盘阈值一致: ≥0.6 喜欢 / ≤0.25 讨厌)。
func attitudeOfWeight(wgt float64) survey.Attitude {
	switch {
	case wgt >= 0.6:
		return survey.Like
	case wgt <= 0.25:
		return survey.Dislike
	default:
		return survey.Neutral
	}
}

// answersFromProfile 从画像反推全部维度的问卷勾选状态(五维三态)。
func answersFromProfile(profile *config.Profile) map[string]survey.Attitude {
	answers := make(map[string]survey.Attitude)
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			answers[config.Key(d.ID, v.ID)] = attitudeOfWeight(profile.Get(d.ID, v.ID))
		}
	}
	return answers
}

// defaultPackName 全新用户默认选中的风格包(避免空白起步, 可再自行调整或清空)。
const defaultPackName = "治愈日常"

// packRowLabel 风格包行的标签文案(向导与设置页共用, 宽度按此文测量锁定;
// 文案刻意短——长文案会在窄窗口折行, 语义已由汇总条/提示行承载)。
const packRowLabel = "风格包:"

// initSelectedPacks 初始选中风格包:
//   - 用户显式选择(profile.Packs 非 nil, 含显式清空)直接采用;
//   - 旧档(packs 字段为 nil)按画像权重推断最匹配的包(运行时推断, 不改写用户数据);
//   - 全新用户(无选择且零喜欢)预选默认包。
func initSelectedPacks(profile *config.Profile) map[string]bool {
	sel := map[string]bool{}
	if profile == nil {
		sel[defaultPackName] = true
		return sel
	}
	if profile.Packs != nil {
		for _, n := range profile.Packs {
			if taxonomy.PackByName(n) != nil {
				sel[n] = true
			}
		}
		return sel
	}
	if likesInProfile(profile) > 0 {
		for _, n := range prompt.InferPacks(profile) {
			sel[n] = true
		}
		return sel
	}
	sel[defaultPackName] = true
	return sel
}

// likesInProfile 统计画像中达到明确偏好阈值的值数。
func likesInProfile(profile *config.Profile) int {
	n := 0
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			if profile.Get(d.ID, v.ID) >= prompt.HighPref {
				n++
			}
		}
	}
	return n
}

// selectedPackNames 按界面顺序返回选中包名; 恒非 nil(落盘后引擎按显式选择执行, 不再推断)。
func selectedPackNames(sel map[string]bool) []string {
	out := []string{}
	for _, p := range taxonomy.PresetPacks {
		if sel[p.Name] {
			out = append(out, p.Name)
		}
	}
	return out
}

// packSelEqual 判断两组选中包是否一致(避免重复落盘/重复信号)。
func packSelEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// copyPackSel 深拷贝选中包快照。
func copyPackSel(sel map[string]bool) map[string]bool {
	out := make(map[string]bool, len(sel))
	for k, v := range sel {
		out[k] = v
	}
	return out
}

// packCovered 该词条(dim/val 键)是否被任一选中风格包覆盖(筹码同步变色用)。
func packCovered(sel map[string]bool, key string) bool {
	dim, val, ok := strings.Cut(key, "/")
	if !ok {
		return false
	}
	for name := range sel {
		p := taxonomy.PackByName(name)
		if p == nil {
			continue
		}
		for _, pv := range p.Values(dim) {
			if pv == val {
				return true
			}
		}
	}
	return false
}

// packSummary 汇总条/回显用的选中包摘要。
func packSummary(sel map[string]bool) string {
	names := selectedPackNames(sel)
	if len(names) == 0 {
		return "风格包: 未选(按喜好自由组合)"
	}
	return "风格包: " + strings.Join(names, " + ")
}

// surveyLikesIn 统计勾选为"喜欢"的项数。
func surveyLikesIn(answers map[string]survey.Attitude) int {
	n := 0
	for _, a := range answers {
		if a == survey.Like {
			n++
		}
	}
	return n
}

// surveyDislikesIn 统计勾选为"讨厌"的项数。
func surveyDislikesIn(answers map[string]survey.Attitude) int {
	n := 0
	for _, a := range answers {
		if a == survey.Dislike {
			n++
		}
	}
	return n
}

// focusSummary 每维取前 2 个喜欢项组成"重点出图"摘要(最多 6 项); 无喜欢项则返回空串。
func focusSummary(answers map[string]survey.Attitude) string {
	var parts []string
	for _, d := range taxonomy.Dimensions {
		n := 0
		for _, v := range d.Values {
			if answers[config.Key(d.ID, v.ID)] == survey.Like {
				parts = append(parts, v.NameCN)
				n++
				if n >= 2 {
					break
				}
			}
		}
	}
	if len(parts) > 6 {
		parts = parts[:6]
	}
	if len(parts) == 0 {
		return ""
	}
	return "  将重点出图: " + strings.Join(parts, " · ")
}

// surveyRecapText 完成页回显所选风格包与偏好(按维度分组); 无包且无喜欢项则只回包状态。
func surveyRecapText(answers map[string]survey.Attitude, packs map[string]bool) string {
	var groups []string
	for _, d := range taxonomy.Dimensions {
		var names []string
		for _, v := range d.Values {
			if answers[config.Key(d.ID, v.ID)] == survey.Like {
				names = append(names, v.NameCN)
			}
		}
		if len(names) > 0 {
			groups = append(groups, d.NameCN+": "+strings.Join(names, " · "))
		}
	}
	lines := []string{packSummary(packs)}
	if len(groups) > 0 {
		lines = append(lines, "额外微调", strings.Join(groups, "\n"))
	}
	return strings.Join(lines, "\n")
}

// surveyEqual 判断两份勾选状态是否完全一致(用于避免重复落盘/重复信号)。
func surveyEqual(a, b map[string]survey.Attitude) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// cloneSurvey 深拷贝勾选状态快照。
func cloneSurvey(m map[string]survey.Attitude) map[string]survey.Attitude {
	out := make(map[string]survey.Attitude, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// phaseFromUser 按用户 ID 稳定分配 0-59 分钟相位(错峰调度, 避免整点集中出图)。
func phaseFromUser(userID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID))
	return int(h.Sum64() % 60)
}

// maxInt 返回两整数较大者。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
