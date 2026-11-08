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
