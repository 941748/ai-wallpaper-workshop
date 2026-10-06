package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.UserID = "u123"
	cfg.CloudURL = "https://example.com"
	cfg.PhaseMinutes = 17
	if err := EnsureSubdirs(dir); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u123" || got.CloudURL != "https://example.com" || got.PhaseMinutes != 17 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.IntervalHours != DefaultIntervalHour || got.Satisfaction.IntervalDays != DefaultAskDays {
		t.Fatalf("defaults lost: %+v", got)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.CloudURL != DefaultCloudURL || !got.QuietEnabled {
		t.Fatalf("unexpected default: %+v", got)
	}
}

func TestScheduleAskSatisfiedGrows(t *testing.T) {
	s := Satisfaction{IntervalDays: DefaultAskDays}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)
	s.ScheduleAsk(now, "satisfied")
	if s.IntervalDays != 4 {
		t.Fatalf("want 4 days, got %d", s.IntervalDays)
	}
	if s.NextAskTime().Sub(now) != 4*24*time.Hour {
		t.Fatalf("next ask at wrong time: %v", s.NextAskTime())
	}
	// 持续递增, 不设上限
	for i := 0; i < 100; i++ {
		s.ScheduleAsk(now, "satisfied")
	}
	if s.IntervalDays != 104 {
		t.Fatalf("want 104 days (no cap), got %d", s.IntervalDays)
	}
}

func TestScheduleAskStyleResetsLaterPostpones(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)
	s := Satisfaction{IntervalDays: 9}
	s.ScheduleAsk(now, "style")
	if s.IntervalDays != DefaultAskDays {
		t.Fatalf("style should reset to %d, got %d", DefaultAskDays, s.IntervalDays)
	}
	s2 := Satisfaction{IntervalDays: 9}
	s2.ScheduleAsk(now, "later")
	if s2.IntervalDays != 9 {
		t.Fatalf("later keeps interval, got %d", s2.IntervalDays)
	}
	if s2.NextAskTime().Sub(now) != 3*24*time.Hour {
		t.Fatalf("later should postpone 3 days, got %v", s2.NextAskTime())
	}
}

func TestProfileRoundTripAndFusion(t *testing.T) {
	dir := t.TempDir()
	p := NewProfile()
	p.Set("style", "ink", 0.9)
	p.Disliked = []string{"cartoon"}
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("style", "ink") != 0.9 {
		t.Fatalf("weight lost: %v", got.Get("style", "ink"))
	}
	if got.Get("style", "realism") != 0.5 {
		t.Fatalf("missing weight should be neutral 0.5, got %v", got.Get("style", "realism"))
	}
	if len(got.Disliked) != 1 || got.Disliked[0] != "cartoon" {
		t.Fatalf("disliked lost: %v", got.Disliked)
	}
	if filepath.Base(dir) == "" {
		t.Fatal("unreachable")
	}
}
