package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

func TestInQuietHours(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 9, 15, h, m, 0, 0, time.Local)
	}
	cases := []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		{"23:00", "07:00", at(23, 30), true},
		{"23:00", "07:00", at(3, 0), true},
		{"23:00", "07:00", at(7, 0), false},
		{"23:00", "07:00", at(12, 0), false},
		{"09:00", "18:00", at(10, 0), true},
		{"09:00", "18:00", at(8, 59), false},
		{"bad", "07:00", at(3, 0), false},
		{"00:00", "00:00", at(0, 0), false},
	}
	for _, c := range cases {
		if got := InQuietHours(c.start, c.end, c.now); got != c.want {
			t.Errorf("InQuietHours(%s,%s,%v)=%v want %v", c.start, c.end, c.now, got, c.want)
		}
	}
}

func makePNG(w, h int, solid bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if solid {
				img.Set(x, y, color.RGBA{10, 20, 30, 255})
			} else {
				img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestFitToScreen(t *testing.T) {
	src := makePNG(1000, 500, false)
	out, err := FitToScreen(src, 400, 400)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 400 {
		t.Fatalf("want 400x400, got %v", b)
	}
	// 常规场景: 16:9 源 → 1920x1080
	out2, err := FitToScreen(makePNG(1344, 768, false), 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	img2, _ := png.Decode(bytes.NewReader(out2))
	if b := img2.Bounds(); b.Dx() != 1920 || b.Dy() != 1080 {
		t.Fatalf("want 1920x1080, got %v", b)
	}
}

func TestInspect(t *testing.T) {
	if err := Inspect(makePNG(512, 512, false)); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	if err := Inspect(makePNG(512, 512, true)); err == nil {
		t.Fatal("solid image should be rejected")
	}
	if err := Inspect(makePNG(64, 64, false)); err == nil {
		t.Fatal("tiny image should be rejected")
	}
	if err := Inspect([]byte("not an image")); err == nil {
		t.Fatal("garbage should be rejected")
	}
}
