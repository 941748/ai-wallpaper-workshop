package policy

import (
	"testing"
	"time"
)

func TestInActive(t *testing.T) {
	p := Default()
	at := func(h, m int) time.Time { return time.Date(2026, 9, 15, h, m, 0, 0, time.Local) }
	cases := []struct {
		h, m int
		want bool
	}{
		{9, 0, true}, {11, 59, true}, {12, 0, false}, {13, 59, false},
		{14, 0, true}, {17, 59, true}, {18, 0, false},
		{20, 0, true}, {23, 59, true}, {0, 0, false}, {8, 59, false},
	}
	for _, c := range cases {
		if got := p.InActive(at(c.h, c.m)); got != c.want {
			t.Errorf("InActive(%02d:%02d)=%v want %v", c.h, c.m, got, c.want)
		}
	}
}

func TestNormalizeAndLabel(t *testing.T) {
	var p Policy
	p = p.Normalize()
	if p.PoolTarget != 3 || len(p.ActiveBlocks) == 0 || len(p.PauseOptions) == 0 || p.MaxPause < 1 {
		t.Fatalf("normalize failed: %+v", p)
	}
	if got := PauseLabel(8); got != "8 小时" {
		t.Fatalf("label(8)=%s", got)
	}
	if got := PauseLabel(24); got != "1 天" {
		t.Fatalf("label(24)=%s", got)
	}
	if got := PauseLabel(48); got != "2 天" {
		t.Fatalf("label(48)=%s", got)
	}
	if got := PauseLabel(36); got != "1 天 12 小时" {
		t.Fatalf("label(36)=%s", got)
	}
	// 非法时段不生效
	bad := Policy{ActiveBlocks: [][2]string{{"25:00", "26:00"}}}
	if bad.InActive(time.Now()) {
		t.Fatal("invalid block should not match")
	}
	// 越界字段回退默认
	n := Policy{PoolTarget: 99, MaxPause: -1}.Normalize()
	if n.PoolTarget != 3 || n.MaxPause != 48 {
		t.Fatalf("out-of-range should fall back: %+v", n)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Default()
	p.PoolTarget = 5
	p.Version = 7
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
	got := Load(dir)
	if got.PoolTarget != 5 || got.Version != 7 {
		t.Fatalf("roundtrip: %+v", got)
	}
	// 文件缺失 → 默认
	if d := Load(t.TempDir()); d.PoolTarget != 3 {
		t.Fatalf("missing file should give default: %+v", d)
	}
}
