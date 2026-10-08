// Package ui 主题基建: 向导/设置面板共用的青绿色板、字体层级与主题化控件。
// 能力边界: walk 原生控件不支持圆角/渐变/阴影, 主题仅用纯色块 + 字体层级 + 文字颜色。
package ui

import (
	"image/color"
	"sync"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// 预览卡片底色/边框(位图绘制用, image/color 类型)。
var (
	cardFill = color.RGBA{249, 250, 249, 255} // 卡片底
	cardLine = color.RGBA{214, 220, 218, 255} // 卡片边框
)

// 色板(青绿强调色主题)。
var (
	colAccent    = walk.RGB(15, 118, 110)  // #0F766E 强调青绿
	colAccentDk  = walk.RGB(11, 93, 87)    // 深青绿(顶栏渐变起点)
	colTitle     = walk.RGB(31, 31, 31)    // 主标题
	colBody      = walk.RGB(51, 51, 51)    // 正文
	colHint      = walk.RGB(140, 140, 140) // 提示灰
	colGreen     = walk.RGB(46, 160, 67)   // 成功/喜欢
	colRed       = walk.RGB(200, 60, 60)   // 失败/讨厌
	colWhite     = walk.RGB(255, 255, 255) // 白
	colNavBg     = walk.RGB(237, 242, 241) // 左导航底(微绿灰)
	colSeparator = walk.RGB(223, 228, 226) // 分隔线
	colPageBg    = walk.RGB(245, 248, 247) // 页面底(浅灰绿, 衬托白色分组卡片)

	// 自绘控件补充色
	colAccentLight       = walk.RGB(226, 241, 239) // 悬停浅青底
	colHeaderSub         = walk.RGB(203, 227, 222) // 顶栏副标题
	colChipBg            = walk.RGB(245, 247, 246) // 筹码底(中立)
	colChipBorder        = walk.RGB(214, 221, 218) // 筹码边框(中立)
	colChipDislikeBg     = walk.RGB(253, 237, 234) // 筹码底(讨厌)
	colChipDislikeBorder = walk.RGB(240, 196, 190) // 筹码边框(讨厌)
)

// 字体层级(Segoe UI; 中文自动回退系统字体)。
var (
	themeFontHeader *walk.Font // 顶栏产品名 14pt bold
	themeFontStep   *walk.Font // 步骤标题 12pt bold
	themeFontGroup  *walk.Font // 分组/汇总数字 10pt bold
	themeFontNav    *walk.Font // 导航项/激活标签 10pt bold
	themeFontReg    *walk.Font // 筹码/普通标签 10pt
	themeFontDone   *walk.Font // 完成页大勾 18pt bold
)

// 常驻画刷/画笔(initTheme 创建, 进程生命周期内保持引用)。
var (
	brushAccent          walk.Brush
	brushAccentDk        walk.Brush
	brushNavBg           walk.Brush
	brushWhite           walk.Brush
	brushPageBg          walk.Brush
	brushSeparator       walk.Brush
	brushAccentLight     walk.Brush
	brushChipBg          walk.Brush
	brushChipDislikeBg   walk.Brush
	penChipBorder        walk.Pen
	penChipDislikeBorder walk.Pen
	penAccent            walk.Pen
)

var themeOnce sync.Once

// initTheme 初始化主题字体与画刷(进程内一次)。
func initTheme() {
	themeOnce.Do(func() {
		themeFontHeader, _ = walk.NewFont("Segoe UI", 14, walk.FontBold)
		themeFontStep, _ = walk.NewFont("Segoe UI", 12, walk.FontBold)
		themeFontGroup, _ = walk.NewFont("Segoe UI", 10, walk.FontBold)
		themeFontNav, _ = walk.NewFont("Segoe UI", 10, walk.FontBold)
		themeFontReg, _ = walk.NewFont("Segoe UI", 10, 0)
		themeFontDone, _ = walk.NewFont("Segoe UI", 18, walk.FontBold)
		brushAccent, _ = walk.NewSolidColorBrush(colAccent)
		brushAccentDk, _ = walk.NewSolidColorBrush(colAccentDk)
		brushNavBg, _ = walk.NewSolidColorBrush(colNavBg)
		brushWhite, _ = walk.NewSolidColorBrush(colWhite)
		brushPageBg, _ = walk.NewSolidColorBrush(colPageBg)
		brushSeparator, _ = walk.NewSolidColorBrush(colSeparator)
		brushAccentLight, _ = walk.NewSolidColorBrush(colAccentLight)
		brushChipBg, _ = walk.NewSolidColorBrush(colChipBg)
		brushChipDislikeBg, _ = walk.NewSolidColorBrush(colChipDislikeBg)
		penChipBorder, _ = walk.NewCosmeticPen(walk.PenSolid, colChipBorder)
		penChipDislikeBorder, _ = walk.NewCosmeticPen(walk.PenSolid, colChipDislikeBorder)
		penAccent, _ = walk.NewCosmeticPen(walk.PenSolid, colAccent)
	})
}

// scaleFunc 返回 96dpi 逻辑单位 → 原生像素的换算闭包(自绘控件混用两套单位时使用)。
func scaleFunc(dpi int) func(int) int {
	if dpi <= 0 {
		dpi = 96
	}
	return func(v int) int { return v * dpi / 96 }
}

// fitWorkArea 将期望的逻辑窗口尺寸裁剪到所在显示器工作区(留 12 单位边距)。
// 依据: 在 150% 缩放的小屏上, 1040x720 逻辑窗口 = 1560x1080 物理像素, 会超出屏幕导致内容被裁。
func fitWorkArea(w walk.Window, want walk.Size) walk.Size {
	dpi := 96
	if wb, ok := w.(interface{ DPI() int }); ok {
		if d := wb.DPI(); d > 0 {
			dpi = d
		}
	}
	var mi win.MONITORINFO
	mi.CbSize = 40 // sizeof(MONITORINFO)
	hmon := win.MonitorFromWindow(w.Handle(), win.MONITOR_DEFAULTTONEAREST)
	if hmon == 0 || !win.GetMonitorInfo(hmon, &mi) {
		return want
	}
	maxW := int(mi.RcWork.Right-mi.RcWork.Left)*96/dpi - 12
	maxH := int(mi.RcWork.Bottom-mi.RcWork.Top)*96/dpi - 12
	if maxW > 200 && want.Width > maxW {
		want.Width = maxW
	}
	if maxH > 200 && want.Height > maxH {
		want.Height = maxH
	}
	return want
}

// themedLabel 新建主题化标签(字体与文字颜色显式指定)。
func themedLabel(parent walk.Container, text string, font *walk.Font, color walk.Color) *walk.Label {
	lbl, _ := walk.NewLabel(parent)
	lbl.SetText(text)
	if font != nil {
		lbl.SetFont(font)
	}
	lbl.SetTextColor(color)
	return lbl
}
