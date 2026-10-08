package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// monitorInfo 对应 Win32 MONITORINFO。
type monitorInfo struct {
	CbSize    uint32
	RcMonitor windows.Rect
	RcWork    windows.Rect
	DwFlags   uint32
}

// IsQuiet 安静检测: 锁屏 / 前台全屏应用(游戏/演示/视频)。
// 任一命中则本轮不换图(仅后台维护: 补池/自更新/策略同步)。
// 注: 换图节律(活跃时段)由 policy 策略控制, 不在此判断。
func IsQuiet() bool {
	return IsSessionLocked() || IsFullscreenForeground()
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
