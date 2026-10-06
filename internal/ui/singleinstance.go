// 单实例保护: 手动运行 exe 时, 已有 GUI 实例则将其窗口带到前台并退出, 避免多开多个向导/设置窗口。
// 注意: 仅约束 GUI 模式; --tick(计划任务静默)与 --apply-update(换装助手)不走此逻辑。
package ui

import (
	"syscall"
	"unsafe"

	"github.com/lxn/win"

	"wallpaper/internal/config"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex  = kernel32.NewProc("CreateMutexW")
	singleInstHandle uintptr
)

// acquireSingleInstance 获取 GUI 单实例互斥体; 已有实例在运行返回 false。
func acquireSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString("Local\\AIWallpaper_GUI_v1")
	if err != nil {
		return true // 极端情况放行, 不阻塞用户
	}
	h, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true
	}
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		_ = win.CloseHandle(win.HANDLE(h))
		return false
	}
	singleInstHandle = h // 进程生命周期内持有(不显式释放)
	return true
}

// activateExistingWindow 将已有实例的向导/设置窗口带到前台。
func activateExistingWindow() {
	for _, title := range []string{
		config.AppNameCN + " — 初始化向导",
		config.AppNameCN + " — 设置",
	} {
		p, err := syscall.UTF16PtrFromString(title)
		if err != nil {
			continue
		}
		if hwnd := win.FindWindow(nil, p); hwnd != 0 {
			win.ShowWindow(hwnd, win.SW_RESTORE)
			win.SetForegroundWindow(hwnd)
			return
		}
	}
}
