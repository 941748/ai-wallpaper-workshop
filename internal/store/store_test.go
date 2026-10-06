package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wallpaper/internal/prompt"
)

func TestLockBusy(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel1, err := s1.Lock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	s2, _ := Open(dir)
	_, err = s2.Lock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock should be busy, got %v", err)
	}
	rel1()
	rel2, err := s2.Lock()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	rel2()
}

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// 直接造大文件触发轮转
	big := make([]byte, LogMaxSize+10)
	if err := os.WriteFile(filepath.Join(dir, "logs", "runtime.log"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	s.Log("after rotation")
	if _, err := os.Stat(filepath.Join(dir, "logs", "runtime.log.1")); err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "logs", "runtime.log"))
	if len(data) == 0 || len(data) > 4096 {
		t.Fatalf("new log unexpected size %d", len(data))
	}
}

func TestCleanupWallpapersProtectsCurrent(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// 造 35 张壁纸, 最旧的一张设为 current
	var paths []string
	for i := 0; i < 35; i++ {
		p, err := s.SaveWallpaper([]byte("x"), time.Now().Add(-time.Duration(35-i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	// 修改文件修改时间来区分新旧(名称带时间戳, 已足够区分)
	current := paths[0] // 最旧
	s.SetCurrent(current)
	if err := s.CleanupWallpapers(KeepWallpapers); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.WallpapersDir())
	count := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".png" {
			count++
		}
	}
	if count > KeepWallpapers+1 { // 允许额外保留 1 张受保护的当前壁纸
		t.Fatalf("too many wallpapers left: %d", count)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current wallpaper must never be deleted: %v", err)
	}
}

func TestHistoryAppendCap(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < MaxHistory+20; i++ {
		if err := s.AppendHistory(prompt.HistoryEntry{
			At:    time.Now(),
			Combo: map[string]string{"style": "ink"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.LoadHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != MaxHistory {
		t.Fatalf("want %d entries, got %d", MaxHistory, len(h))
	}
}
