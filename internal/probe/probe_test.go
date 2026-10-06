package probe

import (
	"testing"

	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

// TestBalancedCombosBalance 校验 12 张首轮组合每维取值尽量均衡(出现次数差 ≤1)。
func TestBalancedCombosBalance(t *testing.T) {
	combos := BalancedCombos(FirstCount)
	if len(combos) != FirstCount {
		t.Fatalf("want %d combos, got %d", FirstCount, len(combos))
	}
	seen := map[string]bool{}
	counts := map[string]map[string]int{} // dim → val → count
	for _, d := range taxonomy.Dimensions {
		counts[d.ID] = map[string]int{}
	}
	for _, c := range combos {
		if seen[c.Key()] {
			t.Fatalf("duplicate combo: %s", c.Key())
		}
		seen[c.Key()] = true
		for dimID, valID := range c.Values {
			counts[dimID][valID]++
		}
	}
	for _, d := range taxonomy.Dimensions {
		minC, maxC := 1<<30, 0
		for _, v := range d.Values {
			c := counts[d.ID][v.ID]
			if c < minC {
				minC = c
			}
			if c > maxC {
				maxC = c
			}
		}
		if maxC-minC > 1 {
			t.Fatalf("dimension %s unbalanced: min=%d max=%d", d.ID, minC, maxC)
		}
		// 词典扩充后, 值数超过探针数的维度无法全覆盖; 只对可覆盖的维度断言
		if len(d.Values) <= FirstCount && minC == 0 {
			t.Fatalf("dimension %s has unused value", d.ID)
		}
	}
}

// TestSurveyCombos 问卷校准组合应使用独立 ID 前缀与轮次, 避免命中云端旧公共池。
func TestSurveyCombos(t *testing.T) {
	combos := SurveyCombos(FirstCount)
	if len(combos) != FirstCount {
		t.Fatalf("want %d combos, got %d", FirstCount, len(combos))
	}
	seen := map[string]bool{}
	for _, c := range combos {
		if c.Kind != SurveyRound {
			t.Fatalf("combo %s kind = %s, want %s", c.ID, c.Kind, SurveyRound)
		}
		if len(c.ID) < 2 || c.ID[0] != 's' {
			t.Fatalf("combo ID %s should use s prefix", c.ID)
		}
		if seen[c.Key()] {
			t.Fatalf("duplicate combo: %s", c.Key())
		}
		seen[c.Key()] = true
	}
}

func TestCombosDeterministicSeed(t *testing.T) {
	a := BalancedCombos(FirstCount)
	b := BalancedCombos(FirstCount)
	for i := range a {
		if a[i].Key() != b[i].Key() || a[i].Seed != b[i].Seed {
			t.Fatalf("combos not deterministic at %d", i)
		}
		if a[i].Prompt() == "" {
			t.Fatalf("prompt empty at %d", i)
		}
	}
}

func TestInferFromChoices(t *testing.T) {
	combos := BalancedCombos(FirstCount)
	// 喜欢包含 ink 的全部组合, 不喜欢包含 animal 的全部组合
	choices := map[string]Choice{}
	for _, c := range combos {
		switch {
		case c.Values[taxonomy.DimStyle] == "ink":
			choices[c.ID] = Like
		case c.Values[taxonomy.DimSubject] == "animal":
			choices[c.ID] = Dislike
		default:
			choices[c.ID] = Neutral
		}
	}
	p := Infer(combos, choices, nil)
	if w := p.Get(taxonomy.DimStyle, "ink"); w <= 0.5 {
		t.Fatalf("liked style should be >0.5, got %v", w)
	}
	if w := p.Get(taxonomy.DimSubject, "animal"); w >= 0.5 {
		t.Fatalf("disliked subject should be <0.5, got %v", w)
	}
	if len(p.Disliked) == 0 {
		t.Fatal("disliked fragments should be recorded")
	}
	// 融合: 已有画像与新结果合并而非覆盖
	prev := config.NewProfile()
	prev.Set(taxonomy.DimStyle, "ink", 0.2)
	merged := Infer(combos, choices, prev)
	w := merged.Get(taxonomy.DimStyle, "ink")
	if w <= 0.2 || w >= p.Get(taxonomy.DimStyle, "ink")+0.001 {
		t.Fatalf("fusion weight out of range: %v (prev 0.2, fresh %v)", w, p.Get(taxonomy.DimStyle, "ink"))
	}
	if merged.Version <= prev.Version {
		t.Fatalf("version should bump: %d → %d", prev.Version, merged.Version)
	}
}

func TestRefineCombosAroundTop(t *testing.T) {
	p := config.NewProfile()
	p.Set(taxonomy.DimStyle, "watercolor", 0.95)
	p.Set(taxonomy.DimPalette, "pastel", 0.9)
	combos := RefineCombos(p, RefineCount)
	if len(combos) != RefineCount {
		t.Fatalf("want %d refine combos, got %d", RefineCount, len(combos))
	}
	seen := map[string]bool{}
	hitTop := 0
	for _, c := range combos {
		if seen[c.Key()] {
			t.Fatalf("duplicate refine combo %s", c.Key())
		}
		seen[c.Key()] = true
		if c.Seed == 0 {
			t.Fatal("refine combo missing seed")
		}
		if c.Values[taxonomy.DimStyle] == "watercolor" && c.Values[taxonomy.DimPalette] == "pastel" {
			hitTop++
		}
	}
	if hitTop == 0 {
		t.Fatal("refine combos should mostly center on top values")
	}
}

func TestSummarizeAndDistance(t *testing.T) {
	p := config.NewProfile()
	p.Set(taxonomy.DimPalette, "cool", 0.9)
	p.Set(taxonomy.DimMood, "majestic", 0.7)
	s := Summarize(p, 5)
	if len(s) < 2 {
		t.Fatalf("summarize too short: %v", s)
	}
	if s[0] != "色调-冷蓝青 0.90" {
		t.Fatalf("unexpected first summary: %s", s[0])
	}
	d := WeightedDistance(
		map[string]string{taxonomy.DimStyle: "ink", taxonomy.DimMood: "serene"},
		map[string]string{taxonomy.DimStyle: "cyberpunk", taxonomy.DimMood: "serene"},
	)
	if d != 0.5 {
		t.Fatalf("want distance 0.5, got %v", d)
	}
}
