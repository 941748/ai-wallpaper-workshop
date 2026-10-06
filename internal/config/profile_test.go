package config

import (
	"fmt"
	"testing"
)

func TestNormalizeKeywords(t *testing.T) {
	in := []string{" 猫咪 ", "猫咪", "", "狗子", "   ", "白描"}
	out := NormalizeKeywords(in)
	want := []string{"猫咪", "狗子", "白描"}
	if len(out) != len(want) {
		t.Fatalf("NormalizeKeywords = %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d]=%q, want %q", i, out[i], want[i])
		}
	}

	long := "水墨山水画风格演示内容超过二十四个字符需要被截断处理掉多余部分"
	out = NormalizeKeywords([]string{long})
	if len(out) != 1 || len([]rune(out[0])) != 24 {
		t.Fatalf("长词应截断为 24 字: %v", out)
	}

	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf("kw%d", i))
	}
	if got := NormalizeKeywords(many); len(got) != 20 {
		t.Fatalf("应限制 20 个, got %d", len(got))
	}
}

func TestProfileCustomKeywordsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := NewProfile()
	p.CustomKeywords = []string{" 猫咪 ", "猫咪", "高达"}
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CustomKeywords) != 2 || got.CustomKeywords[0] != "猫咪" || got.CustomKeywords[1] != "高达" {
		t.Fatalf("round trip keywords = %v", got.CustomKeywords)
	}
}
