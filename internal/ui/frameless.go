package ui

// 无边框窗口(方案 B): 去掉系统标题栏(WS_CAPTION), 保留可拉伸边框(WS_THICKFRAME),
// 借 DWM 扩展帧维持阴影与 Win 键贴靠; 自绘低调标题栏承载:
//   · 左侧产品名 / 右侧副标题
//   · 右上角扁平 ✕(常态低调灰白, 悬停变深)
//   · 标题栏本体作为拖动热区(按住左键拖动整窗)
// 三个顶层窗口(向导 / 设置 / 回访小窗)共用本文件的 titleBar + enableCustomFrame。

import (
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// ---------- DWM 扩展帧(保留阴影) ----------

var (
	dwmapi                           = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

// dwmMargins 对应 Win32 MARGINS。
type dwmMargins struct {
	cxLeftWidth    int32
	cxRightWidth   int32
	cyTopHeight    int32
	cyBottomHeight int32
}

// nccalcsizeParams 对应 Win32 NCCALCSIZE_PARAMS。
type nccalcsizeParams struct {
	rgrc  [3]win.RECT
	lppos uintptr
}

func dwmExtendFrame(hwnd win.HWND, m dwmMargins) {
	procDwmExtendFrameIntoClientArea.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&m)))
}

// enableCustomFrame 将窗口改为无边框(保留阴影/贴靠/边缘拉伸)。
// 须在窗口创建后、内容布局就绪时调用一次。
func enableCustomFrame(form walk.Form) {
	hwnd := form.Handle()
	if hwnd == 0 {
		return
	}
	style := win.GetWindowLong(hwnd, win.GWL_STYLE)
	style &^= win.WS_CAPTION   // 去标题栏(含边框/对话框框)
	style |= win.WS_THICKFRAME // 保留可拉伸边框 → DWM 阴影与贴靠仍生效
	win.SetWindowLong(hwnd, win.GWL_STYLE, style)
	// 顶部留 1px 边距, 令 DWM 继续绘制窗口阴影
	dwmExtendFrame(hwnd, dwmMargins{0, 0, 1, 0})
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_FRAMECHANGED)
	subclassFrame(hwnd)
}

// subclassFrame 子类化窗口过程:
//   - WM_NCCALCSIZE: 移除顶部标题栏区, 保留左右下拉伸边框;
//   - WM_GETMINMAXINFO: 固定尺寸——walk 只写 PtMinTrackSize(下限), 把上限同步为下限,
//     使拖拽与最大化都无法改变窗口尺寸(walk 的 maxSize 从未被消费, 实测能拉大);
//   - WM_NCHITTEST: 边缘/边角的拉伸热区降级为客户区, 光标不再出现拉伸箭头。
func subclassFrame(hwnd win.HWND) {
	var orig uintptr
	cb := windows.NewCallback(func(h uintptr, msg uint32, wp uintptr, lp unsafe.Pointer) uintptr {
		switch msg {
		case win.WM_NCCALCSIZE:
			if wp == 1 {
				p := (*nccalcsizeParams)(lp)
				top := p.rgrc[0].Top
				ret := win.CallWindowProc(orig, win.HWND(h), msg, wp, uintptr(lp))
				if ret != 0 {
					return ret
				}
				// 还原顶边 → 客户区上移到窗口顶端(去掉标题栏), 左右下边框保持默认(可拉伸)
				p.rgrc[0].Top = top
				return 0
			}
		case win.WM_GETMINMAXINFO:
			ret := win.CallWindowProc(orig, win.HWND(h), msg, wp, uintptr(lp))
			if lp != nil {
				mmi := (*win.MINMAXINFO)(lp)
				mmi.PtMaxTrackSize = mmi.PtMinTrackSize // 跟踪上限 = 下限 → 不可拉伸
				mmi.PtMaxSize = mmi.PtMinTrackSize       // 最大化尺寸也封顶 → Win+↑ 无效
			}
			return ret
		case win.WM_NCHITTEST:
			ret := win.CallWindowProc(orig, win.HWND(h), msg, wp, uintptr(lp))
			switch int32(ret) {
			case win.HTLEFT, win.HTRIGHT, win.HTTOP, win.HTTOPLEFT, win.HTTOPRIGHT,
				win.HTBOTTOM, win.HTBOTTOMLEFT, win.HTBOTTOMRIGHT, win.HTBORDER:
				// 固定尺寸窗口: 边缘不再提供拉伸交互(标题条拖动走 HTCAPTION 注入, 不受影响)
				return uintptr(win.HTCLIENT)
			}
			return ret
		}
		return win.CallWindowProc(orig, win.HWND(h), msg, wp, uintptr(lp))
	})
	orig = win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, cb)
}

// ---------- 自绘标题栏(拖动热区 + 扁平 ✕) ----------

// titleBar 无边框窗口顶部的自绘标题栏。
type titleBar struct {
	cw        *walk.CustomWidget
	form      walk.Form
	title     string
	subtitle  string
	onClose   func()
	height    int            // 逻辑高度(96dpi)
	hoverX    bool           // ✕ 悬停态
	closeRect walk.Rectangle // ✕ 命中区(像素, paint 时计算)
}

// newTitleBar 创建标题栏(parent 的布局中作为首个子项); onClose 为 ✕ 点击回调。
func newTitleBar(parent walk.Container, form walk.Form, title, subtitle string, height int, onClose func()) (*titleBar, error) {
	tb := &titleBar{form: form, title: title, subtitle: subtitle, onClose: onClose, height: height}
	cw, err := walk.NewCustomWidgetPixels(parent, 0, tb.paint)
	if err != nil {
		return nil, err
	}
	tb.cw = cw
	cw.MouseDown().Attach(tb.onMouseDown)
	cw.MouseMove().Attach(tb.onMouseMove)
	_ = cw.SetMinMaxSize(walk.Size{Height: height}, walk.Size{Height: height})
	return tb, nil
}

// setSubtitle 更新副标题(向导按步骤变化时可用)。
func (tb *titleBar) setSubtitle(s string) {
	if tb.subtitle != s {
		tb.subtitle = s
		tb.cw.Invalidate()
	}
}

// closeRectNow ✕ 的命中/绘制区(物理像素): 按控件当前尺寸实时计算。
// 不依赖上次绘制的快照——窗口拉伸/DPI 变化后, 若命中区停在旧宽度, 点 ✕ 会落空并
// 误入拖动分支(实测: 拉伸后再点 ✕ 无反应)。
func (tb *titleBar) closeRectNow() walk.Rectangle {
	s := scaleFunc(tb.cw.DPI())
	var rc win.RECT
	if !win.GetClientRect(tb.cw.Handle(), &rc) {
		return walk.Rectangle{}
	}
	w, h := int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
	if w <= 0 || h <= 0 {
		return walk.Rectangle{}
	}
	btnW := s(46)
	if btnW > w {
		btnW = w
	}
	return walk.Rectangle{X: w - btnW, Y: 0, Width: btnW, Height: h}
}

func (tb *titleBar) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	s := scaleFunc(tb.cw.DPI())
	cw := clientWidthPx(tb.cw.Handle())
	if cw <= 0 {
		cw = update.Width
	}
	h := update.Height
	_ = canvas.GradientFillRectanglePixels(colAccentDk, colAccent, walk.Horizontal, update)

	padX := s(22)
	btnW := s(46)
	if r := tb.closeRectNow(); r.Width > 0 {
		tb.closeRect = r
	} else {
		tb.closeRect = walk.Rectangle{X: cw - btnW, Y: 0, Width: btnW, Height: h}
	}
	textW := cw - btnW - padX
	if textW < 0 {
		textW = 0
	}
	_ = canvas.DrawTextPixels(tb.title, themeFontHeader, colWhite,
		walk.Rectangle{X: padX, Y: 0, Width: textW, Height: h}, walk.TextVCenter|walk.TextSingleLine|walk.TextLeft)
	if tb.subtitle != "" {
		_ = canvas.DrawTextPixels(tb.subtitle, themeFontReg, colHeaderSub,
			walk.Rectangle{X: padX, Y: 0, Width: textW - padX, Height: h}, walk.TextVCenter|walk.TextSingleLine|walk.TextRight)
	}
	// ✕: 悬停时铺一层深底并转纯白, 常态低调灰白无底
	xCol := colCloseIdle
	if tb.hoverX {
		_ = canvas.FillRectanglePixels(brushCloseHover, tb.closeRect)
		xCol = colWhite
	}
	_ = canvas.DrawTextPixels("✕", themeFontStep, xCol, tb.closeRect, walk.TextVCenter|walk.TextSingleLine|walk.TextCenter)
	return nil
}

func (tb *titleBar) inClose(x, y int) bool {
	r := tb.closeRectNow() // 实时命中区: 窗口尺寸变化后仍准确
	return r.Width > 0 && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

func (tb *titleBar) onMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.LeftButton {
		return
	}
	if tb.inClose(x, y) {
		if tb.onClose != nil {
			tb.onClose()
		}
		return
	}
	// 拖动整窗: 释放捕获后向窗口发送非客户区左键按下(HTCAPTION), 交由系统移动循环处理
	if hwnd := tb.form.Handle(); hwnd != 0 {
		win.ReleaseCapture()
		win.SendMessage(hwnd, win.WM_NCLBUTTONDOWN, win.HTCAPTION, 0)
	}
}

func (tb *titleBar) onMouseMove(x, y int, _ walk.MouseButton) {
	if hover := tb.inClose(x, y); hover != tb.hoverX {
		tb.hoverX = hover
		tb.cw.Invalidate()
	}
}
