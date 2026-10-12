package ui

// 向导的纯决策逻辑(不依赖 walk 窗口): 问卷三态换算、风格包预设、勾选统计与摘要、
// 完成页回显、相位分配等。抽离至此便于单元测试(见 wizard_test.go), 并让 wizard.go
// 专注于 UI 构建与流程编排。

import (
	"hash/fnv"
	"strings"
	"time"

	"wallpaper/internal/config"
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

// presetPack 一个风格包(面向 90/00 后审美的预设组合)。
type presetPack struct {
	Name string   // 界面按钮文案
	Keys []string // config.Key(dim, val) 列表
}

// presetPacks 风格包(每包覆盖 5 维, 点选即勾为喜欢, 可多选叠加并再微调)。
var presetPacks = []presetPack{
	{Name: "潮酷未来", Keys: []string{
		config.Key(taxonomy.DimStyle, "cyberpunk"),
		config.Key(taxonomy.DimStyle, "neonart"),
		config.Key(taxonomy.DimSubject, "citynight"),
		config.Key(taxonomy.DimSubject, "space"),
		config.Key(taxonomy.DimPalette, "neon"),
		config.Key(taxonomy.DimMood, "vibrant"),
		config.Key(taxonomy.DimComposition, "silhouette"),
	}},
	{Name: "二次元", Keys: []string{
		config.Key(taxonomy.DimStyle, "anime"),
		config.Key(taxonomy.DimStyle, "anime3d"),
		config.Key(taxonomy.DimSubject, "mecha"),
		config.Key(taxonomy.DimSubject, "pet"),
		config.Key(taxonomy.DimPalette, "pastel"),
		config.Key(taxonomy.DimMood, "vibrant"),
		config.Key(taxonomy.DimComposition, "centered"),
	}},
	{Name: "国风雅韵", Keys: []string{
		config.Key(taxonomy.DimStyle, "ink"),
		config.Key(taxonomy.DimStyle, "neochinese"),
		config.Key(taxonomy.DimStyle, "papercut"),
		config.Key(taxonomy.DimSubject, "oldstreet"),
		config.Key(taxonomy.DimSubject, "cloudsea"),
		config.Key(taxonomy.DimPalette, "morandi"),
		config.Key(taxonomy.DimMood, "serene"),
		config.Key(taxonomy.DimComposition, "wide"),
	}},
	{Name: "仙侠问道", Keys: []string{
		config.Key(taxonomy.DimStyle, "ink"),
		config.Key(taxonomy.DimStyle, "neochinese"),
		config.Key(taxonomy.DimSubject, "xianxia"),
		config.Key(taxonomy.DimSubject, "cloudsea"),
		config.Key(taxonomy.DimPalette, "cool"),
		config.Key(taxonomy.DimMood, "serene"),
		config.Key(taxonomy.DimMood, "mysterious"),
		config.Key(taxonomy.DimComposition, "wide"),
		config.Key(taxonomy.DimComposition, "silhouette"),
	}},
	{Name: "治愈日常", Keys: []string{
		config.Key(taxonomy.DimStyle, "film"),
		config.Key(taxonomy.DimStyle, "watercolor"),
		config.Key(taxonomy.DimSubject, "pet"),
		config.Key(taxonomy.DimSubject, "plants"),
		config.Key(taxonomy.DimSubject, "food"),
		config.Key(taxonomy.DimPalette, "warm"),
		config.Key(taxonomy.DimMood, "cozy"),
		config.Key(taxonomy.DimComposition, "macro"),
	}},
	{Name: "极简高级", Keys: []string{
		config.Key(taxonomy.DimStyle, "minimal"),
		config.Key(taxonomy.DimSubject, "abstract"),
		config.Key(taxonomy.DimSubject, "building"),
		config.Key(taxonomy.DimPalette, "dark"),
		config.Key(taxonomy.DimMood, "mysterious"),
		config.Key(taxonomy.DimComposition, "centered"),
	}},
	{Name: "留白呼吸", Keys: []string{
		config.Key(taxonomy.DimStyle, "minimal"),
		config.Key(taxonomy.DimStyle, "neochinese"),
		config.Key(taxonomy.DimSubject, "seasons"),
		config.Key(taxonomy.DimSubject, "lake"),
		config.Key(taxonomy.DimPalette, "morandi"),
		config.Key(taxonomy.DimPalette, "pastel"),
		config.Key(taxonomy.DimMood, "serene"),
		config.Key(taxonomy.DimComposition, "centered"),
		config.Key(taxonomy.DimComposition, "wide"),
	}},
}

// defaultPackName 初始化默认风格包: 全新用户进入问卷时无任何勾选, 预勾该包避免从零点选。
const defaultPackName = "治愈日常"

// applyDefaultPack 将默认风格包内词条勾为"喜欢"。仅在问卷无任何勾选(全新用户)时调用。
func applyDefaultPack(answers map[string]survey.Attitude) {
	for _, p := range presetPacks {
		if p.Name != defaultPackName {
			continue
		}
		for _, k := range p.Keys {
			if _, ok := answers[k]; ok {
				answers[k] = survey.Like
			}
		}
		return
	}
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

// surveyRecapText 完成页回显所选偏好(按维度分组); 无喜欢项则返回空串。
func surveyRecapText(answers map[string]survey.Attitude) string {
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
	if len(groups) == 0 {
		return ""
	}
	return "你的偏好\n" + strings.Join(groups, "\n")
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
