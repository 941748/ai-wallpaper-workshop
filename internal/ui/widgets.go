// Package ui walk 原生界面: 初始化向导 / 设置面板 / 探针网格 / 满意度回访小窗。
// 设计约束: 关闭窗口 = 进程立即退出; 无托盘、无后台服务、无 Web 界面。
package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os/exec"

	"github.com/lxn/walk"

	imgdraw "golang.org/x/image/draw"

	"wallpaper/internal/probe"
)

// 探针状态边框颜色。
var (
	colNeutral = color.RGBA{180, 180, 180, 255}
	colLike    = color.RGBA{46, 160, 67, 255}
	colDislike = color.RGBA{200, 60, 60, 255}
)

// decodePNG 解码 PNG 字节。
func decodePNG(data []byte) (image.Image, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// scaleWithBorder 缩放图片到指定尺寸并叠加状态边框, 返回可显示位图。
func scaleWithBorder(data []byte, w, h int, border color.Color) (*walk.Bitmap, error) {
	src, err := decodePNG(data)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	imgdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	if border != nil {
		drawBorder(dst, border, 4)
	}
	return walk.NewBitmapFromImage(dst)
}

// scalePlain 缩放图片到指定尺寸(无边框)。
func scalePlain(data []byte, maxW, maxH int) (*walk.Bitmap, error) {
	src, err := decodePNG(data)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	w, h := maxW, maxH
	if sb.Dx() > 0 && sb.Dy() > 0 {
		scale := float64(maxW) / float64(sb.Dx())
		if s := float64(maxH) / float64(sb.Dy()); s < scale {
			scale = s
		}
		w = int(float64(sb.Dx()) * scale)
		h = int(float64(sb.Dy()) * scale)
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	imgdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
	return walk.NewBitmapFromImage(dst)
}

// scaleCard 等比缩放图片到指定画布并加卡片式细边框(向导预览用)。
func scaleCard(data []byte, maxW, maxH int) (*walk.Bitmap, error) {
	src, err := decodePNG(data)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	// 等比缩放到画布内(留 10px 内边距)
	const pad = 10
	w, h := maxW-2*pad, maxH-2*pad
	if sb.Dx() > 0 && sb.Dy() > 0 {
		scale := float64(w) / float64(sb.Dx())
		if s := float64(h) / float64(sb.Dy()); s < scale {
			scale = s
		}
		w = int(float64(sb.Dx()) * scale)
		h = int(float64(sb.Dy()) * scale)
	}
	dst := image.NewRGBA(image.Rect(0, 0, maxW, maxH))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: cardFill}, image.Point{}, draw.Src)
	ox := (maxW - w) / 2
	oy := (maxH - h) / 2
	imgdraw.CatmullRom.Scale(dst, image.Rect(ox, oy, ox+w, oy+h), src, sb, draw.Over, nil)
	drawBorder(dst, cardLine, 1)
	return walk.NewBitmapFromImage(dst)
}

func drawBorder(img *image.RGBA, c color.Color, thickness int) {
	b := img.Bounds()
	for t := 0; t < thickness; t++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.Set(x, b.Min.Y+t, c)
			img.Set(x, b.Max.Y-1-t, c)
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			img.Set(b.Min.X+t, y, c)
			img.Set(b.Max.X-1-t, y, c)
		}
	}
}

// addLineRow 在容器内新增一行: 标签 + 单行输入框, 返回输入框。
func addLineRow(parent walk.Container, label, initial string, width int) (*walk.LineEdit, error) {
	row, err := walk.NewComposite(parent)
	if err != nil {
		return nil, err
	}
	if err := row.SetLayout(walk.NewHBoxLayout()); err != nil {
		return nil, err
	}
	lbl, err := walk.NewLabel(row)
	if err != nil {
		return nil, err
	}
	lbl.SetText(label)
	le, err := walk.NewLineEdit(row)
	if err != nil {
		return nil, err
	}
	le.SetText(initial)
	if width > 0 {
		_ = le.SetMinMaxSize(walk.Size{Width: width}, walk.Size{Width: width})
	}
	return le, nil
}

// clearChildren 清空并释放容器内所有子控件(用于分步重建向导内容)。
func clearChildren(c walk.Container) {
	if c == nil {
		return
	}
	children := c.Children()
	for children.Len() > 0 {
		child := children.At(children.Len() - 1)
		child.SetParent(nil) // 置空父级并释放
	}
}

// openFolder 打开资源管理器定位到目录。
func openFolder(dir string) {
	_ = exec.Command("explorer", dir).Start()
}

// ---------- 探针网格 ----------

// probeGrid 12/8 张探针图网格: 单击循环 中立 → 喜欢 → 不喜欢。
type probeGrid struct {
	comp    *walk.Composite
	combos  []probe.Combo
	images  map[string][]byte
	choices map[string]probe.Choice
	views   map[string]*walk.ImageView
	states  map[string]*walk.Label
	onChg   func()
}

// newProbeGrid 构建网格(6 列)。
func newProbeGrid(parent walk.Container, combos []probe.Combo, images map[string][]byte, onChanged func()) (*probeGrid, error) {
	g := &probeGrid{
		combos:  combos,
		images:  images,
		choices: map[string]probe.Choice{},
		views:   map[string]*walk.ImageView{},
		states:  map[string]*walk.Label{},
		onChg:   onChanged,
	}
	for _, c := range combos {
		g.choices[c.ID] = probe.Neutral
	}
	comp, err := walk.NewComposite(parent)
	if err != nil {
		return nil, err
	}
	g.comp = comp
	if err := comp.SetLayout(walk.NewVBoxLayout()); err != nil {
		return nil, err
	}
	const cols = 6
	var row *walk.Composite
	for idx, c := range combos {
		if idx%cols == 0 {
			row, err = walk.NewComposite(comp)
			if err != nil {
				return nil, err
			}
			if err := row.SetLayout(walk.NewHBoxLayout()); err != nil {
				return nil, err
			}
		}
		id := c.ID
		cell, err := walk.NewComposite(row)
		if err != nil {
			return nil, err
		}
		vbox := walk.NewVBoxLayout()
		_ = vbox.SetSpacing(2)
		_ = cell.SetLayout(vbox)

		iv, err := walk.NewImageView(cell)
		if err != nil {
			return nil, err
		}
		_ = iv.SetMinMaxSize(walk.Size{Width: 156, Height: 117}, walk.Size{Width: 156, Height: 117})
		iv.SetCursor(walk.CursorHand())
		iv.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
			if button != walk.LeftButton {
				return
			}
			g.cycle(id)
		})
		g.views[id] = iv

		lbl, err := walk.NewLabel(cell)
		if err != nil {
			return nil, err
		}
		lbl.SetTextAlignment(walk.AlignCenter)
		g.states[id] = lbl

		g.render(id)
	}
	return g, nil
}

// cycle 循环状态并刷新渲染。
func (g *probeGrid) cycle(id string) {
	switch g.choices[id] {
	case probe.Neutral:
		g.choices[id] = probe.Like
	case probe.Like:
		g.choices[id] = probe.Dislike
	default:
		g.choices[id] = probe.Neutral
	}
	g.render(id)
	if g.onChg != nil {
		g.onChg()
	}
}

func (g *probeGrid) render(id string) {
	var border color.Color = colNeutral
	var text = "中立"
	switch g.choices[id] {
	case probe.Like:
		border, text = colLike, "喜欢"
	case probe.Dislike:
		border, text = colDislike, "不喜欢"
	}
	if img, ok := g.images[id]; ok {
		if bmp, err := scaleWithBorder(img, 156, 117, border); err == nil {
			g.views[id].SetImage(bmp)
		}
	}
	g.states[id].SetText(text)
}

// Likes 当前"喜欢"数量。
func (g *probeGrid) Likes() int {
	n := 0
	for _, c := range g.choices {
		if c == probe.Like {
			n++
		}
	}
	return n
}

// Choices 返回点选结果。
func (g *probeGrid) Choices() map[string]probe.Choice { return g.choices }
