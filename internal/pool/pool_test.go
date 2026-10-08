package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func entry(n int) Entry {
	return Entry{
		ProfileVersion: 1, Positive: "p", Seed: int64(n),
		CreatedAt: time.Date(2026, 9, 15, 10, n, 0, 0, time.Local).Format(time.RFC3339),
	}
}

func TestAddTakeFIFO(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := Add(dir, entry(i), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := Count(dir); n != 3 {
		t.Fatalf("count=%d want 3", n)
	}
	e, img, err := Take(dir)
	if err != nil || e == nil {
		t.Fatalf("take: %+v %v", e, err)
	}
	if e.Seed != 0 || len(img) != 1 || img[0] != 0 {
		t.Fatalf("FIFO order broken: %+v", e)
	}
	if n := Count(dir); n != 2 {
		t.Fatalf("count=%d want 2", n)
	}
	_, _, _ = Take(dir)
	_, _, _ = Take(dir)
	e2, img2, err := Take(dir)
	if e2 != nil || img2 != nil || err != nil {
		t.Fatal("empty pool should return nil triple")
	}
}

func TestTakeMissingFileCleansEntry(t *testing.T) {
	dir := t.TempDir()
	if err := Add(dir, entry(0), []byte{1}); err != nil {
		t.Fatal(err)
	}
	// 手动删掉图片文件, 模拟损坏: Take 应清理该条目且不报错
	for _, e := range List(dir) {
		_ = os.Remove(filepath.Join(poolDir(dir), e.File))
	}
	e, img, err := Take(dir)
	if e != nil || img != nil || err != nil {
		t.Fatalf("broken entry should be cleaned silently: %+v %v %v", e, img, err)
	}
	if n := Count(dir); n != 0 {
		t.Fatalf("count=%d want 0", n)
	}
}

func TestCleanVersionAndPending(t *testing.T) {
	dir := t.TempDir()
	_ = Add(dir, entry(0), []byte{1})
	_ = Add(dir, Entry{ProfileVersion: 2, CreatedAt: time.Now().Format(time.RFC3339)}, []byte{2})
	if n := CleanVersion(dir, 1); n != 1 {
		t.Fatalf("clean removed=%d want 1", n)
	}
	if n := Count(dir); n != 1 {
		t.Fatalf("count=%d want 1", n)
	}
	if err := SavePending(dir, Pending{JobID: "j1", ProfileVersion: 1, Since: "t"}); err != nil {
		t.Fatal(err)
	}
	if p := LoadPending(dir); p == nil || p.JobID != "j1" {
		t.Fatalf("pending: %+v", p)
	}
	ClearPending(dir)
	if p := LoadPending(dir); p != nil {
		t.Fatal("pending should be cleared")
	}
}

func TestHardCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxKeep+3; i++ {
		if err := Add(dir, entry(i), []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	if n := Count(dir); n != MaxKeep {
		t.Fatalf("count=%d want %d", n, MaxKeep)
	}
	// 最旧的已被丢弃: 首张 Seed = 3
	es := List(dir)
	if len(es) == 0 || es[0].Seed != 3 {
		t.Fatalf("oldest should be dropped, got %+v", es)
	}
}
