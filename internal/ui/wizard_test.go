package ui

import (
	"strings"
	"testing"

	"wallpaper/internal/survey"
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

func TestPresetKeysCoverAllDims(t *testing.T) {
	dims := map[string]bool{}
	for _, k := range presetKeys {
		dims[strings.SplitN(k, "/", 2)[0]] = true // "dim/value" 前缀即维度
	}
	for _, want := range []string{"style", "subject", "palette", "mood", "composition"} {
		if !dims[want] {
			t.Errorf("一键推荐未覆盖维度 %s", want)
		}
	}
	if len(presetKeys) < 10 || len(presetKeys) > 14 {
		t.Errorf("推荐项数量 %d 不符合预期(约 12)", len(presetKeys))
	}
}
