// 自绘风格包选择行: 一排圆角筹码, 选中 = 强调色填充(与筹码"喜欢"同色), 悬停描边。
// 选中与否完全由颜色表达, 不画勾; 筹码网格中被选中包覆盖的词条用同色同步高亮。
package ui

import (
	"github.com/lxn/walk"
	"github.com/lxn/win"

	"wallpaper/internal/taxonomy"
)

// packRow 风格包多选行(自绘, 替代原生按钮——PushButton 系统绘制无法改色)。
type packRow struct {
	cw        *walk.CustomWidget
	getSel    func() map[string]bool // 实时读取选中包(向导/设置页各自持有数据源)
	onToggle  func(name string)
	hoverName string
	needH     int // 已向 walk 申明的目标高度(0=尚未申明)
	hit       []packChipHit
}

type packChipHit struct {
	name string
	rect walk.Rectangle
}

// packRowH 风格包行逻辑高度(96dpi): 32 筹码 + 上下留白。
const packRowH = 44

// newPackRow 创建风格包选择行。
func newPackRow(parent walk.Container, getSel func() map[string]bool, onToggle func(string)) (*packRow, error) {
	initTheme()
	pr := &packRow{getSel: getSel, onToggle: onToggle}
	cw, err := walk.NewCustomWidgetPixels(parent, 0, pr.paint)
	if err != nil {
		return nil, err
	}
	pr.cw = cw
	cw.MouseDown().Attach(pr.onMouseDown)
	cw.MouseMove().Attach(pr.onMouseMove)
	_ = cw.SetMinMaxSize(walk.Size{Height: packRowH}, walk.Size{Height: packRowH})
	return pr, nil
}

func (pr *packRow) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	dpi := pr.cw.DPI()
	s := scaleFunc(dpi)

	// 构造期的 SetMinMaxSize 会被 walk 布局初始化覆盖(实测控件只剩 25px,
	// 32px 筹码被裁掉下半截); 与 surveyChips 相同, 在 paint 中经 Synchronize
	// 重设高度才可靠——仅首次绘制触发, 之后不再打扰布局。
	if pr.needH != packRowH {
		pr.needH = packRowH
		widget := pr.cw
		widget.Synchronize(func() {
			_ = widget.SetMinMaxSize(walk.Size{Height: packRowH}, walk.Size{Height: packRowH})
		})
	}

	// 按控件实际像素高度自适应: 布局 DPI 换算与绘制缩放不一致(混合缩放多屏)时,
	// 筹码收缩并垂直居中, 保证任何情况下不被上/下沿裁切。
	avail := s(packRowH)
	var rc win.RECT
	if win.GetClientRect(pr.cw.Handle(), &rc) && rc.Bottom > rc.Top {
		avail = int(rc.Bottom - rc.Top)
	}
	pad := s(6)
	chipH := s(chipsChipH)
	if avail > 2*pad+8 {
		if chipH > avail-2*pad {
			chipH = avail - 2*pad
		}
	} else if avail > 4 {
		chipH = avail - 2
	} else {
		chipH = avail
	}
	if chipH < 4 {
		chipH = 4
	}
	chipY := (avail - chipH) / 2
	if chipY < 0 {
		chipY = 0
	}

	sel := pr.getSel()
	corner := walk.Size{Width: chipH / 2, Height: chipH / 2}
	// 标签画进控件自身(而非独立 Label 控件): 本控件是页面 VBox 的直接子项,
	// 与筹码组件同一条布局路径——高分屏(150% 副屏)下 VBox 的 min/max 换算正确,
	// 而 HBox 交叉轴会把行高压乱、连带裁切筹码与上方提示行。
	labelW := textWidthPx(packRowLabel, 10, false, dpi)
	_ = canvas.DrawTextPixels(packRowLabel, themeFontReg, colBody,
		walk.Rectangle{X: s(chipsPadX), Y: 0, Width: labelW + s(8), Height: avail},
		walk.TextLeft|walk.TextVCenter|walk.TextSingleLine)
	x := s(chipsPadX) + labelW + s(16)
	pr.hit = pr.hit[:0]
	for _, p := range taxonomy.PresetPacks {
		w := textWidthPx(p.Name, 10, false, dpi) + s(30)
		rect := walk.Rectangle{X: x, Y: chipY, Width: w, Height: chipH}
		if sel[p.Name] {
			_ = canvas.FillRoundedRectanglePixels(brushAccent, rect, corner)
			_ = canvas.DrawTextPixels(p.Name, themeFontReg, colWhite, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		} else {
			_ = canvas.FillRoundedRectanglePixels(brushChipBg, rect, corner)
			border := penChipBorder
			if p.Name == pr.hoverName {
				border = penAccent
			}
			_ = canvas.DrawRoundedRectanglePixels(border, rect, corner)
			_ = canvas.DrawTextPixels(p.Name, themeFontReg, colBody, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
		pr.hit = append(pr.hit, packChipHit{name: p.Name, rect: rect})
		x += w + s(chipsGapX)
	}
	return nil
}

// hitAt 返回坐标命中的包名; 无则空串。
func (pr *packRow) hitAt(x, y int) string {
	for _, h := range pr.hit {
		if x >= h.rect.X && x < h.rect.X+h.rect.Width && y >= h.rect.Y && y < h.rect.Y+h.rect.Height {
			return h.name
		}
	}
	return ""
}

func (pr *packRow) onMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.LeftButton {
		return
	}
	if name := pr.hitAt(x, y); name != "" && pr.onToggle != nil {
		pr.onToggle(name)
	}
}

func (pr *packRow) onMouseMove(x, y int, _ walk.MouseButton) {
	if h := pr.hitAt(x, y); h != pr.hoverName {
		pr.hoverName = h
		pr.cw.Invalidate()
	}
}

// refresh 选中态或主题变化后重绘。
func (pr *packRow) refresh() { pr.cw.Invalidate() }
