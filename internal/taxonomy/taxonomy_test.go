package taxonomy

import "testing"

func TestDimensionsSanity(t *testing.T) {
	if len(Dimensions) != 5 {
		t.Fatalf("want 5 dimensions, got %d", len(Dimensions))
	}
	seenVal := map[string]bool{}
	for _, d := range Dimensions {
		if d.ID == "" || d.NameCN == "" {
			t.Fatalf("dimension missing id/name: %+v", d)
		}
		if len(d.Values) < 4 || len(d.Values) > 40 {
			t.Fatalf("dimension %s should have 4-40 values, got %d", d.ID, len(d.Values))
		}
		for _, v := range d.Values {
			if v.ID == "" || v.NameCN == "" || v.Prompt == "" {
				t.Fatalf("value incomplete in %s: %+v", d.ID, v)
			}
			key := d.ID + "/" + v.ID
			if seenVal[key] {
				t.Fatalf("duplicate value key %s", key)
			}
			seenVal[key] = true
		}
	}
}

func TestLookupHelpers(t *testing.T) {
	if Get(DimStyle) == nil || Get("nope") != nil {
		t.Fatal("Get broken")
	}
	if v := ValueOf(DimStyle, "ink"); v == nil || v.NameCN != "水墨国风" {
		t.Fatalf("ValueOf broken: %+v", v)
	}
	if ValueOf(DimStyle, "nope") != nil {
		t.Fatal("ValueOf should return nil for unknown")
	}
	if got := NameCN(DimPalette, "cool"); got != "色调-冷蓝青" {
		t.Fatalf("NameCN broken: %s", got)
	}
	if BaseNegative == "" || QualitySuffix == "" {
		t.Fatal("negative/suffix should not be empty")
	}
}
