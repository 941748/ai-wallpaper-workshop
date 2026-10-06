package desktop

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"wallpaper/internal/config"
)

// monitorInfo 对应 Win32 MONITORINFO。
type monitorInfo struct {
	CbSize    uint32
	RcMonitor windows.Rect
	RcWork    windows.Rect
	DwFlags   uint32
}

// IsQuiet 安静检测: 锁屏 / 静默时段 / 前台全屏应用(游戏/演示/视频)。
// 任一命中则本轮不打扰(不换壁纸、不弹回访)。
func IsQuiet(cfg *config.Config, now time.Time) bool {
	if IsSessionLocked() {
		return true
	}
	if cfg.QuietEnabled && InQuietHours(cfg.QuietStart, cfg.QuietEnd, now) {
		return true
	}
	if IsFullscreenForeground() {
		return true
	}
	return false
}

// InQuietHours 判断 now 是否落在 [start, end) 静默时段(支持跨天, 如 23:00~07:00)。
func InQuietHours(start, end string, now time.Time) bool {
	sh, sm, ok1 := parseHHMM(start)
	eh, em, ok2 := parseHHMM(end)
	if !ok1 || !ok2 || (sh == eh && sm == em) {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	s := sh*60 + sm
	e := eh*60 + em
	if s < e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e // 跨天
}

func parseHHMM(s string) (int, int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// IsSessionLocked 通过 OpenInputDesktop 判断当前会话是否锁定。
func IsSessionLocked() bool {
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopSwitchDesktop)
	if h == 0 {
		return true
	}
	procCloseDesktop.Call(h)
	return false
}

// IsFullscreenForeground 判断前台窗口是否全屏(排除桌面/任务栏等 Shell 窗口)。
func IsFullscreenForeground() bool {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	cls := make([]uint16, 64)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
	if n > 0 {
		switch windows.UTF16ToString(cls[:n]) {
		case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
			return false
		}
	}
	var wr windows.Rect
	if r1, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); r1 == 0 {
		return false
	}
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon == 0 {
		return false
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r1, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r1 == 0 {
		return false
	}
	m := mi.RcMonitor
	return wr.Left <= m.Left && wr.Top <= m.Top && wr.Right >= m.Right && wr.Bottom >= m.Bottom
}
