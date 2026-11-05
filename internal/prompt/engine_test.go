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

// ---------- 一图一包采样 ----------

// packProfile 构造仅选中指定风格包、无任何手动点选的画像。
func packProfile(name string) *config.Profile {
	p := config.NewProfile()
	p.Packs = []string{name}
	return p
}

func assertIn(t *testing.T, dim, val string, allowed []string, ctx string) {
	t.Helper()
	for _, a := range allowed {
		if a == val {
			return
		}
	}
	t.Fatalf("%s: %s=%s 越出允许集 %v", ctx, dim, val, allowed)
}

// TestComposePackCoherence 任一采样组合的五维必须整体出自同一个包, 绝不跨包拆混。
func TestComposePackCoherence(t *testing.T) {
	for _, name := range []string{"国风仙侠", "自然山河", "写实美女", "国漫3D", "潮酷未来", "治愈日常", "极简留白"} {
		p := packProfile(name)
		pack := taxonomy.PackByName(name)
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < 200; i++ {
			s := Compose(p, rng, Options{})
			for _, d := range taxonomy.Dimensions {
				assertIn(t, d.ID, s.Combo[d.ID], pack.Values(d.ID), name+"#"+string(rune('0'+i%10)))
			}
		}
	}
}

// TestComposePackSecondaryLock 色调/情绪/构图: 手动喜欢 1 项 = 锁定覆盖包基线。
func TestComposePackSecondaryLock(t *testing.T) {
	p := packProfile("国风仙侠") // 包基线: morandi/serene/wide,closeup
	p.Set(taxonomy.DimPalette, "warm", HighPref)
	p.Set(taxonomy.DimMood, "vibrant", HighPref)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 100; i++ {
		s := Compose(p, rng, Options{})
		if s.Combo[taxonomy.DimPalette] != "warm" {
			t.Fatalf("色调应锁定为手动喜欢的 warm, got %s", s.Combo[taxonomy.DimPalette])
		}
		if s.Combo[taxonomy.DimMood] != "vibrant" {
			t.Fatalf("情绪应锁定为手动喜欢的 vibrant, got %s", s.Combo[taxonomy.DimMood])
		}
		// 未锁定的风格/题材仍守包基线
		assertIn(t, taxonomy.DimStyle, s.Combo[taxonomy.DimStyle], []string{"ink", "neochinese"}, "lock")
		assertIn(t, taxonomy.DimSubject, s.Combo[taxonomy.DimSubject], []string{"xianxia", "oldstreet", "cloudsea"}, "lock")
	}
}

// TestComposePackManualStyleUnion 风格/题材: 手动喜欢并入包轮换池, 其余维不外溢。
func TestComposePackManualStyleUnion(t *testing.T) {
	p := packProfile("潮酷未来")
	p.Set(taxonomy.DimStyle, "ink", HighPref)
	rng := rand.New(rand.NewSource(3))
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s := Compose(p, rng, Options{})
		assertIn(t, taxonomy.DimStyle, s.Combo[taxonomy.DimStyle], []string{"cyberpunk", "neonart", "ink"}, "union")
		assertIn(t, taxonomy.DimSubject, s.Combo[taxonomy.DimSubject], []string{"citynight", "space"}, "union")
		assertIn(t, taxonomy.DimPalette, s.Combo[taxonomy.DimPalette], []string{"neon"}, "union")
		seen[s.Combo[taxonomy.DimStyle]] = true
	}
	if !seen["ink"] {
		t.Fatal("手动并入的风格值未参与轮换")
	}
}

// TestComposePackDislikeExcluded 包内值被点踩即从基线消失。
func TestComposePackDislikeExcluded(t *testing.T) {
	p := packProfile("治愈日常")
	p.Set(taxonomy.DimSubject, "cat", LowCut-0.1)
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 100; i++ {
		s := Compose(p, rng, Options{})
		if s.Combo[taxonomy.DimSubject] == "cat" {
			t.Fatal("点踩值 cat 不应被采出")
		}
		assertIn(t, taxonomy.DimSubject, s.Combo[taxonomy.DimSubject], []string{"dog", "child", "plants", "food"}, "dislike")
	}
}

// TestComposeShakeExcludesLastStyleInPack 包模式下"换个风格"仍在包内排除轮换。
func TestComposeShakeExcludesLastStyleInPack(t *testing.T) {
	p := packProfile("自然山河")
	hist := []HistoryEntry{{At: time.Now(), Combo: map[string]string{taxonomy.DimStyle: "ink"}}}
	for i := 0; i < 30; i++ {
		s := Compose(p, rand.New(rand.NewSource(int64(i))), Options{ForceShake: true, History: hist})
		if s.Combo[taxonomy.DimStyle] == "ink" {
			t.Fatalf("换个风格应排除包内最近主风格 ink, got %s", s.Combo[taxonomy.DimStyle])
		}
		assertIn(t, taxonomy.DimStyle, s.Combo[taxonomy.DimStyle],
			[]string{"realism", "watercolor", "impressionist"}, "shake")
	}
}

// TestInferPacks 整包喜欢 → 推出该包; 零散喜欢不误推; 显式空不推断。
func TestInferPacks(t *testing.T) {
	p := config.NewProfile()
	target := taxonomy.PackByName("治愈日常")
	for _, d := range taxonomy.Dimensions {
		for _, v := range target.Values(d.ID) {
			p.Set(d.ID, v, HighPref)
		}
	}
	names := InferPacks(p)
	found := false
	for _, n := range names {
		if n == "治愈日常" {
			found = true
		}
	}
	if !found {
		t.Fatalf("整包喜欢应推出该包, got %v", names)
	}

	scattered := config.NewProfile()
	scattered.Set(taxonomy.DimStyle, "pixel", HighPref)
	scattered.Set(taxonomy.DimMood, "melancholy", HighPref)
	if names := InferPacks(scattered); len(names) != 0 {
		t.Fatalf("零散喜欢不应推断出包, got %v", names)
	}

	empty := config.NewProfile()
	empty.Packs = []string{}
	if got := activePacks(empty); len(got) != 0 {
		t.Fatalf("显式清空不应推断, got %v", got)
	}
}
