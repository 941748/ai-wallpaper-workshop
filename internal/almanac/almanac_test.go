package almanac

import (
	"testing"
	"time"
)

func hasName(evs []Event, name string) bool {
	for _, e := range evs {
		if e.Name == name {
			return true
		}
	}
	return false
}

func TestWithinWindow(t *testing.T) {
	// 2026 春节前一日: 2/16 起 3 天窗口 → 含春节(2/17)
	now := time.Date(2026, 2, 16, 10, 0, 0, 0, time.Local)
	evs := Within(now, 3)
	if !hasName(evs, "春节") {
		t.Fatalf("春节未命中: %+v", evs)
	}
	// 升序
	for i := 1; i < len(evs); i++ {
		if evs[i].Date.Before(evs[i-1].Date) {
			t.Fatal("事件未按日期升序")
		}
	}
	// 远离窗口不应命中
	now2 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local)
	if hasName(Within(now2, 3), "春节") {
		t.Fatal("春节不应出现在 7 月窗口")
	}
}

func TestMotherFatherDay(t *testing.T) {
	// 2026 母亲节 = 5/10(5 月第二个周日)
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.Local)
	if !hasName(Within(now, 3), "母亲节") {
		t.Fatal("母亲节未命中(应 2026-05-10)")
	}
	// 2026 父亲节 = 6/21(6 月第三个周日)
	now2 := time.Date(2026, 6, 19, 0, 0, 0, 0, time.Local)
	if !hasName(Within(now2, 3), "父亲节") {
		t.Fatal("父亲节未命中(应 2026-06-21)")
	}
}

func TestYearRollover(t *testing.T) {
	// 12/31 窗口跨入新年 → 元旦
	now := time.Date(2026, 12, 31, 9, 0, 0, 0, time.Local)
	if !hasName(Within(now, 2), "元旦") {
		t.Fatal("跨年窗口应命中元旦")
	}
}

func TestSensitiveQingming(t *testing.T) {
	// 清明标记 Sensitive(提醒非庆祝)
	now := time.Date(2026, 4, 4, 0, 0, 0, 0, time.Local)
	found := false
	for _, e := range Within(now, 2) {
		if e.Name == "清明" {
			found = true
			if !e.Sensitive {
				t.Fatal("清明应为敏感节令")
			}
		}
	}
	if !found {
		t.Fatal("清明未命中")
	}
}

func TestDefaultWindow(t *testing.T) {
	// days<=0 使用默认 3 天窗口: 3/19 起应覆盖 3/20 春分
	now := time.Date(2026, 3, 19, 0, 0, 0, 0, time.Local)
	if !hasName(Within(now, 0), "春分") {
		t.Fatal("默认窗口应命中春分")
	}
}
