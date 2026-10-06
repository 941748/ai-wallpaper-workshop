package prompt

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

func newProfile() *config.Profile {
	p := config.NewProfile()
	p.Set(taxonomy.DimStyle, "ink", 0.9)
	p.Set(taxonomy.DimPalette, "cool", 0.85)
	p.Disliked = []string{"cartoon", "watermark"}
	return p
}

func comboKeyOf(m map[string]string) string { return comboKey(m) }

func TestComposeDeterministicWithSeed(t *testing.T) {
	p := newProfile()
	s1 := Compose(p, rand.New(rand.NewSource(42)), Options{})
	s2 := Compose(p, rand.New(rand.NewSource(42)), Options{})
	if comboKeyOf(s1.Combo) != comboKeyOf(s2.Combo) || s1.Seed != s2.Seed {
		t.Fatal("compose should be deterministic for same rng seed")
	}
	if s1.Positive == "" || s1.Negative == "" {
		t.Fatal("positive/negative should not be empty")
	}
}

func TestComposeNegativeFusion(t *testing.T) {
	p := newProfile()
	s := Compose(p, rand.New(rand.NewSource(1)), Options{})
	for _, frag := range []string{"cartoon", "watermark"} {
		if !contains(s.Negative, frag) {
			t.Fatalf("negative missing disliked fragment %q: %s", frag, s.Negative)
		}
	}
	if !contains(s.Positive, taxonomy.QualitySuffix) {
		t.Fatal("positive missing quality suffix")
	}
}

func TestComposeNoRepeatWithinWindow(t *testing.T) {
	p := newProfile()
	rng := rand.New(rand.NewSource(7))
	var hist []HistoryEntry
	for i := 0; i < 80; i++ {
		s := Compose(p, rng, Options{History: hist})
		// 最近 30 条内不允许重复
		start := 0
		if len(hist) > RecentWindow {
			start = len(hist) - RecentWindow
		}
		key := comboKeyOf(s.Combo)
		for _, h := range hist[start:] {
			if comboKey(h.Combo) == key {
				t.Fatalf("iteration %d produced duplicate within window: %s", i, key)
			}
		}
		hist = append(hist, HistoryEntry{At: time.Now(), Combo: s.Combo, Seed: s.Seed})
	}
}

func TestComposeShakeExcludesLastStyle(t *testing.T) {
	p := newProfile()
	hist := []HistoryEntry{{
		At:    time.Now(),
		Combo: map[string]string{taxonomy.DimStyle: "ink"},
	}}
	for i := 0; i < 30; i++ {
		s := Compose(p, rand.New(rand.NewSource(int64(i))), Options{ForceShake: true, History: hist})
		if s.Combo[taxonomy.DimStyle] == "ink" {
			t.Fatalf("shake turn must exclude last style value (ink), got %v", s.Combo)
		}
		if !s.ShakeTurn {
			t.Fatal("ShakeTurn flag should be set")
		}
	}
}

func TestComposeExploresSometimes(t *testing.T) {
	p := newProfile()
	rng := rand.New(rand.NewSource(99))
	top, other := 0, 0
	for i := 0; i < 400; i++ {
		s := Compose(p, rng, Options{})
		if s.Combo[taxonomy.DimMood] == "serene" { // 全维同权时 serene 为并列最高, 统计含它
			top++
		} else {
			other++
		}
	}
	if other == 0 {
		t.Fatal("exploration should pick non-top values at least sometimes")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// ---------- 自定义关键词 ----------

func TestComposeCustomKeywords(t *testing.T) {
	p := newProfile()
	p.CustomKeywords = []string{"猫咪"}
	rng := rand.New(rand.NewSource(42))
	hit := 0
	const n = 300
	for i := 0; i < n; i++ {
		spec := Compose(p, rng, Options{})
		if contains(spec.Positive, "猫咪") {
			hit++
		}
	}
	if hit == 0 {
		t.Fatal("自定义关键词从未融入提示词")
	}
	if hit > n/2 {
		t.Fatalf("自定义关键词出现过于频繁(%d/%d)", hit, n)
	}

	// Options 显式传参优先级(云端 localCompose 路径)
	ok := false
	for i := 0; i < n && !ok; i++ {
		if contains(Compose(p, rng, Options{CustomKeywords: []string{"高达"}}).Positive, "高达") {
			ok = true
		}
	}
	if !ok {
		t.Fatal("Options 自定义关键词未生效")
	}
}
