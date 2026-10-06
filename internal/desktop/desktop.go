// Package desktop 桌面集成: 屏幕分辨率探测、图像适配缩放、Windows 换壁纸、安静检测。
package desktop

import "golang.org/x/sys/windows"

// 共享的 Win32 入口。
var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procGetClassNameW         = user32.NewProc("GetClassNameW")
	procMonitorFromWindow     = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW       = user32.NewProc("GetMonitorInfoW")
	procOpenInputDesktop      = user32.NewProc("OpenInputDesktop")
	procCloseDesktop          = user32.NewProc("CloseDesktop")
)

// 系统度量与常量。
const (
	smCXScreen = 0 // SM_CXSCREEN
	smCYScreen = 1 // SM_CYSCREEN

	spiSetDeskWallpaper = 0x0014 // SPI_SETDESKWALLPAPER
	spifUpdateINIFile   = 0x0001
	spifSendWinIniChange = 0x0002

	monitorDefaultToNearest = 0x00000002 // MONITOR_DEFAULTTONEAREST
	desktopSwitchDesktop    = 0x0100     // DESKTOP_SWITCHDESKTOP
)
