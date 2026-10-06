package survey

import (
	"testing"

	"wallpaper/internal/config"
	"wallpaper/internal/taxonomy"
)

func TestApplyMapsThreeStates(t *testing.T) {
	p := config.NewProfile()
	answers := map[string]Attitude{
		config.Key(taxonomy.DimStyle, "ink"):      Like,
		config.Key(taxonomy.DimStyle, "cyberpunk"): Dislike,
		config.Key(taxonomy.DimSubject, "nature"):  Neutral,
	}
	Apply(p, answers)
	if got := p.Get(taxonomy.DimStyle, "ink"); got != LikeWeight {
		t.Fatalf("like weight = %v, want %v", got, LikeWeight)
	}
	if got := p.Get(taxonomy.DimStyle, "cyberpunk"); got != DislikeWeight {
		t.Fatalf("dislike weight = %v, want %v", got, DislikeWeight)
	}
	if got := p.Get(taxonomy.DimSubject, "nature"); got != NeutralWeight {
		t.Fatalf("neutral weight = %v, want %v", got, NeutralWeight)
	}
}

// TestApplyFillsUntouchedWithNeutral 未勾选的项一律显式写入中立权重, 保证问卷画像完全确定。
func TestApplyFillsUntouchedWithNeutral(t *testing.T) {
	p := config.NewProfile()
	Apply(p, nil)
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			if got := p.Get(d.ID, v.ID); got != NeutralWeight {
				t.Fatalf("%s/%s = %v, want %v", d.ID, v.ID, got, NeutralWeight)
			}
		}
	}
}

func TestApplyRecordsDislikedFragment(t *testing.T) {
	p := config.NewProfile()
	Apply(p, map[string]Attitude{config.Key(taxonomy.DimStyle, "realism"): Dislike})
	neg := taxonomy.ValueOf(taxonomy.DimStyle, "realism").Negative
	if neg == "" {
		t.Fatal("realism should have a negative fragment")
	}
	found := false
	for _, f := range p.Disliked {
		if f == neg {
			found = true
		}
	}
	if !found {
		t.Fatalf("negative fragment %q not recorded, got %v", neg, p.Disliked)
	}
}

func TestApplyIgnoresUnknownKeys(t *testing.T) {
	p := config.NewProfile()
	Apply(p, map[string]Attitude{"nope/nope": Like})
	if _, ok := p.Weights["nope/nope"]; ok {
		t.Fatal("unknown answer key must not leak into weights")
	}
}

func TestApplyBumpsVersion(t *testing.T) {
	p := config.NewProfile()
	v0 := p.Version
	Apply(p, nil)
	if p.Version != v0+1 {
		t.Fatalf("version should bump: %d → %d", v0, p.Version)
	}
}
