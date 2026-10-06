// 自绘步骤导航栏。
// 背景: 原生 Label 的文本区域会被其内部 static 子窗口吞掉鼠标消息(工具提示转发后即被消费,
// 既不触发 Label 事件也不冒泡到父 Composite), 无法可靠接收点击; 自绘 CustomWidget 没有此问题。
package ui

import (
	"fmt"

	"github.com/lxn/walk"
)

// navRowHeight 导航行高(96dpi 逻辑单位)。
const navRowHeight = 46

// navSidebar 自绘步骤导航(激活行圆角青底白字 / 完成行绿勾可点 / 未解锁行灰字)。
type navSidebar struct {
	cw      *walk.CustomWidget
	titles  []string
	cur     int
	max     int // 已解锁的最大步骤索引
	hover   int // 悬停行, -1 无
	onClick func(int)
}

// newNavSidebar 创建固定尺寸的导航栏(宽 width, 高随行数)。
func newNavSidebar(parent walk.Container, width int, titles []string, onClick func(int)) (*navSidebar, error) {
	sb := &navSidebar{titles: titles, hover: -1, onClick: onClick}
	cw, err := walk.NewCustomWidgetPixels(parent, 0, sb.paint)
	if err != nil {
		return nil, err
	}
	sb.cw = cw
	cw.MouseDown().Attach(sb.onMouseDown)
	cw.MouseMove().Attach(sb.onMouseMove)
	h := 24 + len(titles)*navRowHeight
	_ = cw.SetMinMaxSize(walk.Size{Width: width, Height: h}, walk.Size{Width: width, Height: h})
	return sb, nil
}

func (sb *navSidebar) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	s := scaleFunc(sb.cw.DPI())
	cw := clientWidthPx(sb.cw.Handle())
	if cw <= 0 {
		cw = update.Width
	}
	padTop, padX, rowGap := s(12), s(12), s(6)
	rowH := s(navRowHeight)
	fmtv := walk.TextVCenter | walk.TextSingleLine | walk.TextLeft
	for i, title := range sb.titles {
		top := padTop + i*rowH
		bounds := walk.Rectangle{X: padX, Y: top, Width: cw - 2*padX, Height: rowH - rowGap}
		numX := padX + s(16)
		numBounds := walk.Rectangle{X: numX, Y: top, Width: s(24), Height: bounds.Height}
		txtBounds := walk.Rectangle{X: numX + s(26), Y: top, Width: bounds.Width - s(60), Height: bounds.Height}
		switch {
		case i == sb.cur:
			_ = canvas.FillRoundedRectanglePixels(brushAccent, bounds, walk.Size{Width: s(8), Height: s(8)})
			_ = canvas.DrawTextPixels(fmt.Sprintf("%d", i+1), themeFontNav, colWhite, numBounds, fmtv)
			_ = canvas.DrawTextPixels(title, themeFontNav, colWhite, txtBounds, fmtv)
		case i < sb.max:
			if sb.hover == i {
				_ = canvas.FillRoundedRectanglePixels(brushAccentLight, bounds, walk.Size{Width: s(8), Height: s(8)})
			}
			_ = canvas.DrawTextPixels("✓", themeFontNav, colGreen, numBounds, fmtv)
			_ = canvas.DrawTextPixels(title, themeFontNav, colTitle, txtBounds, fmtv)
		default:
			_ = canvas.DrawTextPixels(fmt.Sprintf("%d", i+1), themeFontNav, colHint, numBounds, fmtv)
			_ = canvas.DrawTextPixels(title, themeFontNav, colHint, txtBounds, fmtv)
		}
	}
	return nil
}

// rowAt 由 y 像素坐标返回行号; 不在行内返回 -1。
func (sb *navSidebar) rowAt(y int) int {
	s := scaleFunc(sb.cw.DPI())
	idx := (y - s(12)) / s(navRowHeight)
	if idx < 0 || idx >= len(sb.titles) {
		return -1
	}
	return idx
}

func (sb *navSidebar) onMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.LeftButton {
		return
	}
	if row := sb.rowAt(y); row >= 0 && sb.onClick != nil {
		sb.onClick(row)
	}
}

func (sb *navSidebar) onMouseMove(x, y int, button walk.MouseButton) {
	row := sb.rowAt(y)
	hover := -1
	if row >= 0 && row < sb.max { // 仅可点击(已完成)行有悬停反馈
		hover = row
	}
	if hover != sb.hover {
		sb.hover = hover
		sb.cw.Invalidate()
	}
}

// set 刷新当前步骤与解锁进度。
func (sb *navSidebar) set(cur, max int) {
	sb.cur, sb.max = cur, max
	sb.hover = -1
	sb.cw.Invalidate()
}
