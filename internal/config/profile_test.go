package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// TestProfilePacksRoundTrip 校验 packs 字段三态: 旧档缺字段=nil(待推断),
// 显式选择=列表, 显式清空=非 nil 空数组(不推断)。空数组不可因序列化丢失语义。
func TestProfilePacksRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 旧档: 手写无 packs 字段的 profile.json → 加载后 Packs 应为 nil
	legacy := []byte(`{"version":1,"weights":{},"disliked":[]}`)
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Packs != nil {
		t.Fatalf("旧档缺 packs 字段应为 nil, got %v", got.Packs)
	}

	// 显式选择 round trip
	got.Packs = []string{"治愈日常", "自然山河"}
	if err := got.Save(dir); err != nil {
		t.Fatal(err)
	}
	got2, err := LoadProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Packs) != 2 || got2.Packs[0] != "治愈日常" || got2.Packs[1] != "自然山河" {
		t.Fatalf("显式选择 round trip = %v", got2.Packs)
	}

	// 显式清空 round trip(非 nil 空数组语义须保留)
	got2.Packs = []string{}
	if err := got2.Save(dir); err != nil {
		t.Fatal(err)
	}
	got3, err := LoadProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got3.Packs == nil || len(got3.Packs) != 0 {
		t.Fatalf("显式清空 round trip 后应为非 nil 空数组, got %v", got3.Packs)
	}
}
