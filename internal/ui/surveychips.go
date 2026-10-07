// 自绘偏好问卷: 顶部维度标签 + 三态圆角筹码网格。
// 交互: 点筹码循环 中立→喜欢→讨厌; 点维度标签切换; 悬停高亮。
package ui

import (
	"fmt"

	"github.com/lxn/walk"

	"wallpaper/internal/config"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// surveyChips 五维三态问卷(自绘, 替代原生复选框/TabWidget)。
type surveyChips struct {
	cw       *walk.CustomWidget
	answers  map[string]survey.Attitude // 单一数据源(wizard.surveyAnswers)
	dimIdx   int
	hoverKey string // 悬停筹码 key
	hoverTab int    // 悬停维度标签, -1 无
	onChange func()
	onDim    func(int) // 维度切换回调(供向导记住标签位置, 返回重进时恢复)

	chipHit []chipHitRect
	tabHit  []walk.Rectangle
	lastH   int // 最近绘制所需高度(像素), 用于自适应窗高
}

type chipHitRect struct {
	key  string
	rect walk.Rectangle
}

// 布局常量(96dpi 逻辑单位)。
const (
	chipsTabH   = 44
	chipsChipH  = 32
	chipsGapX   = 10
	chipsGapY   = 12
	chipsPadX   = 6
	chipsPadTop = 22
)

// newSurveyChips 创建问卷控件(answers 为共享数据源, 点击直接改动)。
func newSurveyChips(parent walk.Container, answers map[string]survey.Attitude, onChange func()) (*surveyChips, error) {
	// 自绘组件自带主题资源初始化(设置页等非向导环境也会使用, 不能依赖调用方先 initTheme)
	initTheme()
	sc := &surveyChips{answers: answers, hoverTab: -1, onChange: onChange}
	cw, err := walk.NewCustomWidgetPixels(parent, 0, sc.paint)
	if err != nil {
		return nil, err
	}
	sc.cw = cw
	cw.MouseDown().Attach(sc.onMouseDown)
	cw.MouseMove().Attach(sc.onMouseMove)
	_ = cw.SetMinMaxSize(walk.Size{Height: 280}, walk.Size{Height: 280})
	return sc, nil
}

func (sc *surveyChips) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	dpi := sc.cw.DPI()
	s := scaleFunc(dpi)
	tabH := s(chipsTabH)
	// 布局宽度取客户区全宽(update 可能只是部分无效区, 不适用于换行决策)
	cw := clientWidthPx(sc.cw.Handle())
	if cw <= 0 {
		cw = update.Width
	}

	// ---- 维度标签行 ----
	sc.tabHit = sc.tabHit[:0]
	x := s(chipsPadX)
	for i, d := range taxonomy.Dimensions {
		likes := 0
		for _, v := range d.Values {
			if sc.answers[config.Key(d.ID, v.ID)] == survey.Like {
				likes++
			}
		}
		label := d.NameCN
		if likes > 0 {
			label = fmt.Sprintf("%s %d", d.NameCN, likes)
		}
		// 按激活态(加粗)测宽, 保证切换标签时行宽稳定(10pt 主题正文字号)
		tw := textWidthPx(label, 10, true, dpi)
		cell := walk.Rectangle{X: x, Y: 0, Width: tw + s(26), Height: tabH}
		if i == sc.dimIdx {
			_ = canvas.DrawTextPixels(label, themeFontNav, colAccent, cell, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
			_ = canvas.FillRectanglePixels(brushAccent, walk.Rectangle{X: x + s(8), Y: tabH - s(5), Width: cell.Width - s(16), Height: s(3)})
		} else {
			textCol := colBody
			if i == sc.hoverTab {
				textCol = colAccent
			}
			_ = canvas.DrawTextPixels(label, themeFontReg, textCol, cell, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
		sc.tabHit = append(sc.tabHit, cell)
		x += cell.Width + s(6)
	}
	_ = canvas.FillRectanglePixels(brushSeparator, walk.Rectangle{X: 0, Y: tabH - s(1), Width: cw, Height: s(1)})

	// ---- 筹码网格(流式布局, 按宽度换行) ----
	d := taxonomy.Dimensions[sc.dimIdx]
	cy := tabH + s(chipsPadTop)
	cx := s(chipsPadX)
	chipH := s(chipsChipH)
	corner := walk.Size{Width: chipH / 2, Height: chipH / 2}
	sc.chipHit = sc.chipHit[:0]
	for _, v := range d.Values {
		key := config.Key(d.ID, v.ID)
		att := sc.answers[key]
		prefix := ""
		switch att {
		case survey.Like:
			prefix = "✓ "
		case survey.Dislike:
			prefix = "✕ "
		}
		full := prefix + v.NameCN
		chipW := textWidthPx(full, 10, false, dpi) + s(30)
		if cx+chipW > cw-s(chipsPadX) && cx > s(chipsPadX) {
			cx = s(chipsPadX)
			cy += chipH + s(chipsGapY)
		}
		rect := walk.Rectangle{X: cx, Y: cy, Width: chipW, Height: chipH}
		switch att {
		case survey.Like:
			_ = canvas.FillRoundedRectanglePixels(brushAccent, rect, corner)
			_ = canvas.DrawTextPixels(full, themeFontReg, colWhite, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		case survey.Dislike:
			_ = canvas.FillRoundedRectanglePixels(brushChipDislikeBg, rect, corner)
			_ = canvas.DrawRoundedRectanglePixels(penChipDislikeBorder, rect, corner)
			_ = canvas.DrawTextPixels(full, themeFontReg, colRed, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		default:
			_ = canvas.FillRoundedRectanglePixels(brushChipBg, rect, corner)
			border := penChipBorder
			if key == sc.hoverKey {
				border = penAccent
			}
			_ = canvas.DrawRoundedRectanglePixels(border, rect, corner)
			_ = canvas.DrawTextPixels(full, themeFontReg, colBody, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
		sc.chipHit = append(sc.chipHit, chipHitRect{key: key, rect: rect})
		cx += chipW + s(chipsGapX)
	}

	// ---- 自适应高度(内容变化时经 Synchronize 调整, 避免 paint 中递归布局) ----
	// 护栏: 常规内容最大约 7 行; 若测量异常导致超限, 钳位防止把主窗口撑大。
	needH := cy + chipH + s(16)
	if maxH := s(500); needH > maxH {
		needH = maxH
	}
	if needH != sc.lastH {
		sc.lastH = needH
		widget := sc.cw
		need96 := needH * 96 / maxInt(dpi, 1)
		widget.Synchronize(func() {
			_ = widget.SetMinMaxSize(walk.Size{Height: need96}, walk.Size{Height: need96})
		})
	}
	return nil
}

// hitChip 返回坐标命中的筹码。
func (sc *surveyChips) hitChip(x, y int) (string, bool) {
	for _, c := range sc.chipHit {
		if x >= c.rect.X && x < c.rect.X+c.rect.Width && y >= c.rect.Y && y < c.rect.Y+c.rect.Height {
			return c.key, true
		}
	}
	return "", false
}

func (sc *surveyChips) onMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.LeftButton {
		return
	}
	s := scaleFunc(sc.cw.DPI())
	if y < s(chipsTabH) {
		for i, r := range sc.tabHit {
			if x >= r.X && x < r.X+r.Width {
				if sc.dimIdx != i {
					sc.dimIdx = i
					if sc.onDim != nil {
						sc.onDim(i)
					}
					sc.cw.Invalidate()
				}
				return
			}
		}
		return
	}
	if key, ok := sc.hitChip(x, y); ok {
		sc.answers[key] = cycleAttitude(sc.answers[key])
		if sc.onChange != nil {
			sc.onChange()
		}
		sc.cw.Invalidate()
	}
}

func (sc *surveyChips) onMouseMove(x, y int, button walk.MouseButton) {
	s := scaleFunc(sc.cw.DPI())
	hoverKey := ""
	hoverTab := -1
	if y < s(chipsTabH) {
		for i, r := range sc.tabHit {
			if x >= r.X && x < r.X+r.Width {
				hoverTab = i
				break
			}
		}
	} else if key, ok := sc.hitChip(x, y); ok {
		hoverKey = key
	}
	if hoverKey != sc.hoverKey || hoverTab != sc.hoverTab {
		sc.hoverKey, sc.hoverTab = hoverKey, hoverTab
		sc.cw.Invalidate()
	}
}

// cycleAttitude 三态循环: 中立 → 喜欢 → 讨厌 → 中立。
func cycleAttitude(a survey.Attitude) survey.Attitude {
	switch a {
	case survey.Neutral:
		return survey.Like
	case survey.Like:
		return survey.Dislike
	default:
		return survey.Neutral
	}
}

// currentDim 当前维度 ID。
func (sc *surveyChips) currentDim() string { return taxonomy.Dimensions[sc.dimIdx].ID }

// refresh 外部改动 answers 后重绘(一键推荐/清空本维/探针校准融合)。
func (sc *surveyChips) refresh() { sc.cw.Invalidate() }
