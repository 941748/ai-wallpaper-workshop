package scheduler

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/unicode"
)

func TestStartBoundaryPhase(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 30, 0, 0, time.Local)
	// 相位 17: 今天 10:17 已过 → 下一个整点小时的 17 分
	b := StartBoundary(now, 17)
	want := time.Date(2026, 9, 15, 11, 17, 0, 0, time.Local)
	if !b.Equal(want) {
		t.Fatalf("want %v, got %v", want, b)
	}
	// 相位 45: 还没到 → 今天 10:45
	b2 := StartBoundary(now, 45)
	want2 := time.Date(2026, 9, 15, 10, 45, 0, 0, time.Local)
	if !b2.Equal(want2) {
		t.Fatalf("want %v, got %v", want2, b2)
	}
}

func TestBuildXML(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)
	raw, err := BuildXML(`C:\Users\u\AppData\Local\AIWallpaper\bin\wallpaper.exe`, 1, 17, now)
	if err != nil {
		t.Fatal(err)
	}
	// UTF-16LE BOM 检查
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatal("XML must start with UTF-16LE BOM")
	}
	dec := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
	text, err := dec.String(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<Interval>PT1H</Interval>",
		"<StartBoundary>2026-09-15T10:17:00</StartBoundary>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<ExecutionTimeLimit>PT15M</ExecutionTimeLimit>",
		"<LogonType>InteractiveToken</LogonType>",
		"<Priority>7</Priority>",
		"<Arguments>--tick</Arguments>",
		"wallpaper.exe",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("XML missing %q:\n%s", want, text)
		}
	}
	// 间隔可配
	raw2, _ := BuildXML("x.exe", 2, 0, now)
	text2, _ := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().String(string(raw2))
	if !strings.Contains(text2, "<Interval>PT2H</Interval>") {
		t.Fatal("interval not configurable")
	}
}
