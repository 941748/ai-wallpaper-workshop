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

// TestSaveWallpaperNaming 留档命名: jobId 非空 → {jobID}.png; 为空回退时间戳且同秒追加序号。
func TestSaveWallpaperNaming(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)

	p1, err := s.SaveWallpaper("abc123def456", []byte("x"), at)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != "abc123def456.png" {
		t.Fatalf("jobId naming wrong: %s", p1)
	}

	p2, err := s.SaveWallpaper("", []byte("x"), at)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := s.SaveWallpaper("", []byte("x"), at)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p2) != "wallpaper_20261109_120000.png" ||
		filepath.Base(p3) != "wallpaper_20261109_120000_1.png" {
		t.Fatalf("timestamp naming wrong: %s / %s", p2, p3)
	}
}

// TestLocalFavsRoundtrip 本地收藏清单: 无文件空表、增/删。
func TestLocalFavsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadLocalFavs(dir)
	if err != nil || len(m) != 0 {
		t.Fatalf("empty load: %v err=%v", m, err)
	}
	if err := SetLocalFav(dir, "wallpaper_a.png", true); err != nil {
		t.Fatal(err)
	}
	if err := SetLocalFav(dir, "wallpaper_b.png", true); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadLocalFavs(dir)
	if !m["wallpaper_a.png"] || !m["wallpaper_b.png"] || len(m) != 2 {
		t.Fatalf("favs=%v", m)
	}
	if err := SetLocalFav(dir, "wallpaper_a.png", false); err != nil {
		t.Fatal(err)
	}
	m, _ = LoadLocalFavs(dir)
	if m["wallpaper_a.png"] || len(m) != 1 {
		t.Fatalf("favs after delete=%v", m)
	}
}
