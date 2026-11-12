package ui

import (
	"strings"
	"testing"

	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// 问卷/风格包纯逻辑单测(不依赖 walk 窗口)。
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
	if len(taxonomy.PresetPacks) != 9 {
		t.Fatalf("风格包数量 %d, 应为 9(2026-11-10 定稿)", len(taxonomy.PresetPacks))
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
			{taxonomy.DimStyle, pack.Style, 1, 4},
			{taxonomy.DimSubject, pack.Subject, 2, 11}, // 自然风光 11 个(2026-11-12 负责人扩容)
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
		if total < 7 || total > 22 {
			t.Errorf("风格包 %s 词条总数 %d 不符合预期(7~22)", pack.Name, total)
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

// TestDictToSurvey 服务端词典转换: covers 拆维、坏格式忽略、中文名与顺序保持。
func TestDictToSurvey(t *testing.T) {
	d := &cloud.Dict{
		Dimensions: []cloud.DictDim{
			{ID: "style", Name: "风格", Values: []cloud.DictVal{{ID: "ink", Name: "水墨国风"}}},
			{ID: "mood", Name: "情绪", Values: []cloud.DictVal{{ID: "serene", Name: "宁静"}}},
		},
		Packs: []cloud.DictPack{{Name: "心如止水", Covers: []string{"style/ink", "mood/serene", "bad-no-slash"}}},
	}
	dims, packs := dictToSurvey(d)
	if len(dims) != 2 || dims[0].NameCN != "风格" || dims[0].Values[0].NameCN != "水墨国风" {
		t.Fatalf("维度转换错误: %+v", dims)
	}
	if len(packs) != 1 || packs[0].Name != "心如止水" {
		t.Fatalf("包转换错误: %+v", packs)
	}
	if got := packs[0].Vals["style"]; len(got) != 1 || got[0] != "ink" {
		t.Fatalf("covers 拆分错误: %v", packs[0].Vals)
	}
	if _, bad := packs[0].Vals["bad-no-slash"]; bad {
		t.Fatal("无斜杠的坏 covers 应被忽略")
	}
}

// TestSurveyDictLocalFallback 无云端客户端(断网兜底)时回退本地词典。
func TestSurveyDictLocalFallback(t *testing.T) {
	dims, packs := surveyDict(nil)
	if len(dims) != len(taxonomy.Dimensions) {
		t.Fatalf("本地回退维度数 %d, want %d", len(dims), len(taxonomy.Dimensions))
	}
	if len(packs) != len(taxonomy.PresetPacks) {
		t.Fatalf("本地回退包数 %d, want %d", len(packs), len(taxonomy.PresetPacks))
	}
}

// TestGetSurveyPackNameRoundTrip 选中包原名往返: 本地词典没有的包名不被过滤、摘要与回显保留。
func TestGetSurveyPackNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := config.NewProfile()
	p.Packs = []string{"心如止水", "本地不存在的包"}
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{dir: dir, cfg: &config.Config{}}
	got := b.GetSurvey()
	if len(got.SelectedPacks) != 2 || got.SelectedPacks[0] != "心如止水" || got.SelectedPacks[1] != "本地不存在的包" {
		t.Fatalf("SelectedPacks 应原样往返, got %v", got.SelectedPacks)
	}
	if !strings.Contains(got.Summary, "本地不存在的包") {
		t.Fatalf("摘要应含未知包名: %q", got.Summary)
	}
	if !strings.Contains(got.Recap, "本地不存在的包") {
		t.Fatalf("回显应含未知包名: %q", got.Recap)
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
	got := surveyRecapNames(map[string]survey.Attitude{}, nil)
	if !strings.Contains(got, "未选") {
		t.Errorf("无包无喜欢应回显未选状态, got %q", got)
	}
	d0 := taxonomy.Dimensions[0]
	answers := map[string]survey.Attitude{config.Key(d0.ID, d0.Values[0].ID): survey.Like}
	got = surveyRecapNames(answers, []string{defaultPackName})
	if !strings.Contains(got, defaultPackName) || !strings.Contains(got, "额外微调") || !strings.Contains(got, d0.NameCN) {
		t.Errorf("回显应含包名/微调分组/维度名, got %q", got)
	}
}

// TestPackSummary 校验选中包摘要文案。
func TestPackSummary(t *testing.T) {
	if got := packSummaryNames(nil); !strings.Contains(got, "未选") {
		t.Errorf("空选中应提示未选, got %q", got)
	}
	got := packSummaryNames([]string{"潮酷未来", defaultPackName})
	if !strings.Contains(got, "潮酷未来") || !strings.Contains(got, defaultPackName) {
		t.Errorf("摘要应含全部选中包名, got %q", got)
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
