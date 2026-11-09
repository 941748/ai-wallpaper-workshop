package ui

import (
	"strings"
	"testing"

	"wallpaper/internal/config"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// 向导纯逻辑单测(不依赖 walk 窗口)。
func TestAttitudeOfWeight(t *testing.T) {
	cases := []struct {
		wgt  float64
		want survey.Attitude
	}{
		{0.9, survey.Like},
		{0.6, survey.Like},
		{0.61, survey.Like},
		{0.3, survey.Neutral},
		{0.5, survey.Neutral},
		{0.55, survey.Neutral},
		{0.25, survey.Dislike},
		{0.05, survey.Dislike},
		{0, survey.Dislike},
	}
	for _, c := range cases {
		if got := attitudeOfWeight(c.wgt); got != c.want {
			t.Errorf("attitudeOfWeight(%v) = %v, want %v", c.wgt, got, c.want)
		}
	}
}

func TestSurveyEqual(t *testing.T) {
	a := map[string]survey.Attitude{"style/realism": survey.Like, "mood/cozy": survey.Dislike}
	if !surveyEqual(a, cloneSurvey(a)) {
		t.Fatal("相同内容应判等")
	}
	if surveyEqual(a, nil) || surveyEqual(nil, a) {
		t.Fatal("与 nil 不应判等")
	}
	b := cloneSurvey(a)
	b["style/realism"] = survey.Neutral
	if surveyEqual(a, b) {
		t.Fatal("不同内容不应判等")
	}
	c := cloneSurvey(a)
	delete(c, "mood/cozy")
	if surveyEqual(a, c) {
		t.Fatal("长度不同不应判等")
	}
	if !surveyEqual(nil, nil) {
		t.Fatal("nil 与 nil 应判等")
	}
}

func TestPresetPacksTemplate(t *testing.T) {
	if len(taxonomy.PresetPacks) != 7 {
		t.Fatalf("风格包数量 %d, 应为 7(定稿)", len(taxonomy.PresetPacks))
	}
	seen := map[string]bool{}
	for _, pack := range taxonomy.PresetPacks {
		if seen[pack.Name] {
			t.Errorf("风格包名重复 %q", pack.Name)
		}
		seen[pack.Name] = true
		if taxonomy.PackByName(pack.Name) == nil {
			t.Errorf("PackByName 找不到 %q", pack.Name)
		}
		checks := []struct {
			dim  string
			vals []string
			minN int
			maxN int
		}{
			{taxonomy.DimStyle, pack.Style, 2, 4},
			{taxonomy.DimSubject, pack.Subject, 2, 5},
			{taxonomy.DimPalette, pack.Palette, 1, 2},
			{taxonomy.DimMood, pack.Mood, 1, 3},
			{taxonomy.DimComposition, pack.Composition, 1, 3},
		}
		total := 0
		for _, c := range checks {
			if len(c.vals) < c.minN || len(c.vals) > c.maxN {
				t.Errorf("风格包 %s 维度 %s 词条数 %d 不符模板(%d~%d)", pack.Name, c.dim, len(c.vals), c.minN, c.maxN)
			}
			total += len(c.vals)
			if len(pack.Values(c.dim)) != len(c.vals) {
				t.Errorf("风格包 %s Values(%s) 与字段不一致", pack.Name, c.dim)
			}
			for _, v := range c.vals {
				if taxonomy.ValueOf(c.dim, v) == nil {
					t.Errorf("风格包 %s 含非法词条 %s/%s", pack.Name, c.dim, v)
				}
			}
		}
		if total < 7 || total > 16 {
			t.Errorf("风格包 %s 词条总数 %d 不符合预期(7~16)", pack.Name, total)
		}
	}
	if !seen[defaultPackName] {
		t.Errorf("默认风格包 %q 不在定稿包列表中", defaultPackName)
	}
}

// TestPackValuesAccessor 校验 Pack.Values 维度映射与未知维度兜底。
func TestPackValuesAccessor(t *testing.T) {
	p := taxonomy.PresetPacks[0]
	if len(p.Values(taxonomy.DimStyle)) == 0 || p.Values("nope") != nil {
		t.Error("Values 维度映射异常")
	}
	if taxonomy.PackByName("不存在的包") != nil {
		t.Error("未知包名应返回 nil")
	}
}

// TestSurveyCounts 校验喜欢/讨厌计数纯函数。
func TestSurveyCounts(t *testing.T) {
	a := map[string]survey.Attitude{
		"style/a": survey.Like,
		"style/b": survey.Like,
		"mood/x":  survey.Dislike,
		"mood/y":  survey.Neutral,
	}
	if n := surveyLikesIn(a); n != 2 {
		t.Errorf("surveyLikesIn = %d, want 2", n)
	}
	if n := surveyDislikesIn(a); n != 1 {
		t.Errorf("surveyDislikesIn = %d, want 1", n)
	}
	if n := surveyLikesIn(nil); n != 0 {
		t.Errorf("surveyLikesIn(nil) = %d, want 0", n)
	}
}

// TestFocusSummary 校验"重点出图"摘要: 无喜欢项为空, 有则含前导文案。
func TestFocusSummary(t *testing.T) {
	if got := focusSummary(map[string]survey.Attitude{}); got != "" {
		t.Errorf("无喜欢项应为空串, got %q", got)
	}
	d0, d1 := taxonomy.Dimensions[0], taxonomy.Dimensions[1]
	answers := map[string]survey.Attitude{
		config.Key(d0.ID, d0.Values[0].ID): survey.Like,
		config.Key(d1.ID, d1.Values[0].ID): survey.Like,
	}
	got := focusSummary(answers)
	if !strings.Contains(got, "将重点出图") {
		t.Errorf("有喜欢项摘要应含前导文案, got %q", got)
	}
	if !strings.Contains(got, d0.Values[0].NameCN) {
		t.Errorf("摘要应含第一个喜欢词条, got %q", got)
	}
}

// TestSurveyRecapText 校验完成页回显: 恒含包摘要; 有喜欢项按维度分组。
func TestSurveyRecapText(t *testing.T) {
	got := surveyRecapText(map[string]survey.Attitude{}, map[string]bool{})
	if !strings.Contains(got, "未选") {
		t.Errorf("无包无喜欢应回显未选状态, got %q", got)
	}
	d0 := taxonomy.Dimensions[0]
	answers := map[string]survey.Attitude{config.Key(d0.ID, d0.Values[0].ID): survey.Like}
	got = surveyRecapText(answers, map[string]bool{defaultPackName: true})
	if !strings.Contains(got, defaultPackName) || !strings.Contains(got, "额外微调") || !strings.Contains(got, d0.NameCN) {
		t.Errorf("回显应含包名/微调分组/维度名, got %q", got)
	}
}

// TestPackSummary 校验选中包摘要文案。
func TestPackSummary(t *testing.T) {
	if got := packSummary(map[string]bool{}); !strings.Contains(got, "未选") {
		t.Errorf("空选中应提示未选, got %q", got)
	}
	got := packSummary(map[string]bool{"潮酷未来": true, defaultPackName: true})
	if !strings.Contains(got, "潮酷未来") || !strings.Contains(got, defaultPackName) {
		t.Errorf("摘要应含全部选中包名, got %q", got)
	}
}

// TestPackCovered 校验"词条被选中包覆盖"判定(筹码同步变色依据)。
func TestPackCovered(t *testing.T) {
	sel := map[string]bool{"潮酷未来": true}
	if !packCovered(sel, "style/cyberpunk") {
		t.Error("包内词条应判覆盖")
	}
	if !packCovered(sel, "composition/wide") {
		t.Error("包内构图词条应判覆盖")
	}
	if packCovered(sel, "style/ink") {
		t.Error("包外词条不应判覆盖")
	}
	if packCovered(map[string]bool{}, "style/cyberpunk") {
		t.Error("无选中包时不应覆盖")
	}
	if packCovered(sel, "bad-key-no-slash") {
		t.Error("非法键不应判覆盖")
	}
	if packCovered(map[string]bool{"不存在的包": true}, "style/ink") {
		t.Error("失效包名应被跳过, 不应误判覆盖")
	}
}

// TestAnswersFromProfile 校验画像反推: 结构覆盖全部维度词条, 高/低权重映射到喜欢/讨厌。
func TestAnswersFromProfile(t *testing.T) {
	p := config.NewProfile()
	d0 := taxonomy.Dimensions[0]
	p.Set(d0.ID, d0.Values[0].ID, 0.9) // → 喜欢
	p.Set(d0.ID, d0.Values[1].ID, 0.0) // → 讨厌
	answers := answersFromProfile(p)
	total := 0
	for _, d := range taxonomy.Dimensions {
		total += len(d.Values)
	}
	if len(answers) != total {
		t.Errorf("answers 条数 = %d, want %d", len(answers), total)
	}
	if got := answers[config.Key(d0.ID, d0.Values[0].ID)]; got != survey.Like {
		t.Errorf("高权重反推 = %v, want Like", got)
	}
	if got := answers[config.Key(d0.ID, d0.Values[1].ID)]; got != survey.Dislike {
		t.Errorf("低权重反推 = %v, want Dislike", got)
	}
}

// TestInitSelectedPacks 校验初始选中包四分支: 全新用户默认包 / 显式选择 / 旧档推断 / 显式清空不推断。
func TestInitSelectedPacks(t *testing.T) {
	// 1) 全新用户(nil 画像) → 默认包
	sel := initSelectedPacks(nil)
	if !sel[defaultPackName] || len(sel) != 1 {
		t.Errorf("nil 画像应仅预选默认包, got %v", sel)
	}

	// 2) 显式选择 → 原样采用(含失效包名过滤)
	p := config.NewProfile()
	p.Packs = []string{"潮酷未来", "不存在的包", defaultPackName}
	sel = initSelectedPacks(p)
	if !sel["潮酷未来"] || !sel[defaultPackName] || len(sel) != 2 {
		t.Errorf("显式选择应被采用并过滤失效名, got %v", sel)
	}

	// 3) 旧档(packs=nil)且有喜欢 → 按权重推断(整包喜欢 → 推出该包)
	p = config.NewProfile()
	var target *taxonomy.Pack
	for i := range taxonomy.PresetPacks {
		if taxonomy.PresetPacks[i].Name == defaultPackName {
			target = &taxonomy.PresetPacks[i]
		}
	}
	if target == nil {
		t.Fatal("找不到默认包")
	}
	for _, d := range taxonomy.Dimensions {
		for _, v := range target.Values(d.ID) {
			p.Set(d.ID, v, survey.LikeWeight)
		}
	}
	sel = initSelectedPacks(p)
	if !sel[defaultPackName] {
		t.Errorf("旧档整包喜欢应推断出 %q, got %v", defaultPackName, sel)
	}

	// 4) 显式清空(空数组) → 不推断, 即使权重与整包一致
	p.Packs = []string{}
	sel = initSelectedPacks(p)
	if len(sel) != 0 {
		t.Errorf("显式清空不应推断, got %v", sel)
	}
}

// TestSelectedPackNamesOrder 校验落盘包名顺序与非 nil 契约。
func TestSelectedPackNamesOrder(t *testing.T) {
	names := selectedPackNames(map[string]bool{defaultPackName: true, "潮酷未来": true})
	if names == nil || len(names) != 2 {
		t.Fatalf("应返回 2 个包名且非 nil, got %v", names)
	}
	if names[0] != "潮酷未来" { // 界面顺序: 潮酷未来在治愈日常之前
		t.Errorf("应按定稿顺序返回, got %v", names)
	}
	if empty := selectedPackNames(map[string]bool{}); empty == nil || len(empty) != 0 {
		t.Errorf("空选中应返回非 nil 空切片, got %v", empty)
	}
}

// TestPhaseFromUser 校验相位分配: 落在 0~59 且同 ID 稳定。
func TestPhaseFromUser(t *testing.T) {
	p := phaseFromUser("u_abc123")
	if p < 0 || p >= 60 {
		t.Errorf("phaseFromUser 超出 [0,60): %d", p)
	}
	if q := phaseFromUser("u_abc123"); q != p {
		t.Errorf("同 ID 应稳定: %d != %d", q, p)
	}
}
