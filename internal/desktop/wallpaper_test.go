package desktop

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
)

// TestToBMP 验证 PNG → BMP 转码: 尺寸与像素颜色一致(逐像素比较)。
func TestToBMP(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wallpaper_x.png")
	// 3x2 纯色块测试图(不透明)。
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 50), uint8(y * 80), uint8(x + y), 255})
		}
	}
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := toBMP(src)
	if err != nil {
		t.Fatalf("toBMP: %v", err)
	}
	want := filepath.Join(dir, "current.bmp")
	if got != want {
		t.Fatalf("路径不符: 期望 %s, 实际 %s", want, got)
	}

	rf, err := os.Open(got)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	decoded, err := bmp.Decode(rf)
	if err != nil {
		t.Fatalf("BMP 解码: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("尺寸不符: %v", b)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			r, g, bb, _ := decoded.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 != uint32(x*50) || g>>8 != uint32(y*80) || bb>>8 != uint32(x+y) {
				t.Fatalf("像素 (%d,%d) 颜色不符: 期望 (%d,%d,%d), 实际 (%d,%d,%d)",
					x, y, x*50, y*80, x+y, r>>8, g>>8, bb>>8)
			}
		}
	}
}

// TestToBMPBadInput 验证非图片文件与缺失文件返回错误。
func TestToBMPBadInput(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "not-an-image.png")
	if err := os.WriteFile(bad, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := toBMP(bad); err == nil {
		t.Fatal("期望解码失败, 实际成功")
	}
	if _, err := toBMP(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("期望文件缺失报错, 实际成功")
	}
}
