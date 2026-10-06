// 文本精确测量(GDI GetTextExtentPoint32)。
// 背景: walk 的 Canvas.MeasureTextPixels 在本版本返回输入矩形宽度而非文本实际宽度(内部未用 DT_CALCRECT),
// 无法用于自适应布局; 这里自建与主题字体同规格的 HFONT, 直接向 GDI 查询文本像素宽度。
package ui

import (
	"sync"
	"unicode/utf16"

	"github.com/lxn/win"
)

var (
	measMu    sync.Mutex
	measDC    win.HDC
	measFonts = map[measFontKey]win.HFONT{}
)

type measFontKey struct {
	pt   int
	bold bool
	dpi  int
}

// textWidthPx 返回 text 在 Segoe UI(pt 磅, 可选加粗)于 dpi 分辨率下的像素宽度。
func textWidthPx(text string, pt int, bold bool, dpi int) int {
	if text == "" {
		return 0
	}
	measMu.Lock()
	defer measMu.Unlock()
	if measDC == 0 {
		measDC = win.CreateCompatibleDC(0)
		if measDC == 0 {
			return estimateTextWidth(text, pt, dpi)
		}
	}
	hf := measHFONT(pt, bold, dpi)
	if hf == 0 {
		return estimateTextWidth(text, pt, dpi)
	}
	old := win.SelectObject(measDC, win.HGDIOBJ(hf))
	u := utf16.Encode([]rune(text))
	var sz win.SIZE
	ok := win.GetTextExtentPoint32(measDC, &u[0], int32(len(u)), &sz)
	win.SelectObject(measDC, old)
	if !ok || sz.CX <= 0 {
		return estimateTextWidth(text, pt, dpi)
	}
	return int(sz.CX)
}

// measHFONT 创建/缓存与主题字体同规格的 GDI 字体句柄(与 walk Font.createForDPI 参数一致)。
func measHFONT(pt int, bold bool, dpi int) win.HFONT {
	key := measFontKey{pt: pt, bold: bold, dpi: dpi}
	if h, ok := measFonts[key]; ok {
		return h
	}
	var lf win.LOGFONT
	lf.LfHeight = -win.MulDiv(int32(pt), int32(dpi), 72)
	lf.LfWeight = win.FW_NORMAL
	if bold {
		lf.LfWeight = win.FW_BOLD
	}
	lf.LfCharSet = win.DEFAULT_CHARSET
	lf.LfOutPrecision = win.OUT_TT_PRECIS
	lf.LfClipPrecision = win.CLIP_DEFAULT_PRECIS
	lf.LfQuality = win.CLEARTYPE_QUALITY
	lf.LfPitchAndFamily = win.VARIABLE_PITCH | win.FF_SWISS
	copy(lf.LfFaceName[:], utf16.Encode([]rune("Segoe UI")))
	h := win.CreateFontIndirect(&lf)
	measFonts[key] = h
	return h
}

// clientWidthPx 返回窗口客户区宽度(像素)。
func clientWidthPx(hwnd win.HWND) int {
	var r win.RECT
	if !win.GetClientRect(hwnd, &r) {
		return 0
	}
	return int(r.Right - r.Left)
}

// estimateTextWidth 兜底估算(仅当 GDI 调用失败): CJK ≈ 1em, 空格 ≈ 0.3em, 其余 ≈ 0.55em。
func estimateTextWidth(text string, pt, dpi int) int {
	em := float64(pt) * float64(dpi) / 72
	w := 0.0
	for _, r := range text {
		switch {
		case r >= 0x2E80:
			w += em
		case r == ' ':
			w += em * 0.3
		default:
			w += em * 0.55
		}
	}
	return int(w + 0.5)
}
