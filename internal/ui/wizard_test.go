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

func TestCycleAttitude(t *testing.T) {
	if got := cycleAttitude(survey.Neutral); got != survey.Like {
		t.Errorf("中立应循环到喜欢, got %v", got)
	}
	if got := cycleAttitude(survey.Like); got != survey.Dislike {
		t.Errorf("喜欢应循环到讨厌, got %v", got)
	}
	if got := cycleAttitude(survey.Dislike); got != survey.Neutral {
		t.Errorf("讨厌应循环到中立, got %v", got)
	}
}

func TestPresetPacksCoverAllDims(t *testing.T) {
	if len(presetPacks) < 4 || len(presetPacks) > 8 {
		t.Fatalf("风格包数量 %d 不符合预期(4~8)", len(presetPacks))
	}
	for _, pack := range presetPacks {
		dims := map[string]bool{}
		for _, k := range pack.Keys {
			parts := strings.SplitN(k, "/", 2)
			dims[parts[0]] = true
			if len(parts) != 2 || taxonomy.ValueOf(parts[0], parts[1]) == nil {
				t.Errorf("风格包 %s 含非法词条 %s", pack.Name, k)
			}
		}
		for _, want := range []string{"style", "subject", "palette", "mood", "composition"} {
			if !dims[want] {
				t.Errorf("风格包 %s 未覆盖维度 %s", pack.Name, want)
			}
		}
		if len(pack.Keys) < 6 || len(pack.Keys) > 9 {
			t.Errorf("风格包 %s 词条数 %d 不符合预期(6~9)", pack.Name, len(pack.Keys))
		}
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

// TestSurveyRecapText 校验完成页回显: 无喜欢项为空, 有则按维度分组。
func TestSurveyRecapText(t *testing.T) {
	if got := surveyRecapText(map[string]survey.Attitude{}); got != "" {
		t.Errorf("无喜欢项应为空串, got %q", got)
	}
	d0 := taxonomy.Dimensions[0]
	answers := map[string]survey.Attitude{config.Key(d0.ID, d0.Values[0].ID): survey.Like}
	got := surveyRecapText(answers)
	if !strings.Contains(got, "你的偏好") || !strings.Contains(got, d0.NameCN) {
		t.Errorf("回显应含标题与维度名, got %q", got)
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
