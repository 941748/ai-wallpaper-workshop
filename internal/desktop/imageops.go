package desktop

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"golang.org/x/image/draw"
)

// FitToScreen 将 PNG 成品适配到目标分辨率: CatmullRom 缩放 + 中心裁切, 返回 PNG 字节。
func FitToScreen(data []byte, targetW, targetH int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("成品图解码失败: %w", err)
	}
	out := fitCenter(img, targetW, targetH)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fitCenter 等比缩放到完全覆盖目标尺寸(缺边补足)后中心裁切。
func fitCenter(src image.Image, tw, th int) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 {
		return src
	}
	scale := float64(tw) / float64(sw)
	if s := float64(th) / float64(sh); s > scale {
		scale = s
	}
	nw, nh := int(float64(sw)*scale+0.5), int(float64(sh)*scale+0.5)
	if nw < tw {
		nw = tw
	}
	if nh < th {
		nh = th
	}
	scaled := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, sb, draw.Over, nil)
	ox := (nw - tw) / 2
	oy := (nh - th) / 2
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(dst, dst.Bounds(), scaled, image.Point{X: ox, Y: oy}, draw.Src)
	return dst
}

// Inspect 成品质量自检: 可解码、尺寸达标、非纯色。
func Inspect(data []byte) error {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("图像无法解码: %w", err)
	}
	b := img.Bounds()
	if b.Dx() < 256 || b.Dy() < 256 {
		return fmt.Errorf("图像尺寸过小: %dx%d", b.Dx(), b.Dy())
	}
	xs := []int{0, b.Dx() / 2, b.Dx() - 1}
	ys := []int{0, b.Dy() / 2, b.Dy() - 1}
	var first [4]uint32
	set := false
	same := true
	for _, x := range xs {
		for _, y := range ys {
			r, g, bb, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			cur := [4]uint32{r >> 8, g >> 8, bb >> 8, a >> 8}
			if !set {
				first, set = cur, true
				continue
			}
			if cur != first {
				same = false
			}
		}
	}
	if same {
		return fmt.Errorf("图像疑似纯色")
	}
	return nil
}
