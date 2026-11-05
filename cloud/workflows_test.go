package main

import "testing"

// TestBucketSize 屏幕原生优先: 2.2K 屏原生出图; 超上限等比缩放不变形; 无屏值兜底 1920x1080。
func TestBucketSize(t *testing.T) {
	wf := Workflow{MinSide: 512, MaxSide: 2560}

	w, h := BucketSize(2240, 1400, wf)
	if w != 2240 || h != 1400 {
		t.Fatalf("2240x1400 应原生出图, got %dx%d", w, h)
	}

	w, h = BucketSize(3840, 2160, wf)
	if w != 2560 || h != 1440 {
		t.Fatalf("4K 应等比缩到上限内, got %dx%d", w, h)
	}

	w, h = BucketSize(0, 0, wf)
	if w != 1920 || h != 1080 {
		t.Fatalf("无屏值应兜底 1920x1080, got %dx%d", w, h)
	}

	w, h = BucketSize(1493, 933, wf)
	if w%8 != 0 || h%8 != 0 {
		t.Fatalf("应 8 倍数对齐, got %dx%d", w, h)
	}
}
