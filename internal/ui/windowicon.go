package ui

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// setupWindowIcon 运行时设置窗口标题栏/Alt-Tab 图标。
//
// 背景: Wails 建窗后按资源 ID 3 加载图标(winc.AppIconID), 但 rsrc 的 ID 计数器是
// 全局递增 —— manifest=1 + 7 张 RT_ICON=2..8 → 图标组实际落在 ID 9, LoadIcon(3) 失败
// → 标题栏空白(文件资源管理器图标不受影响, 它取图标组本身)。winc 为 internal 包无法
// 调 SetAppIcon, 故建窗后用 exe 的 shell 首图标直接 WM_SETICON 兜底。
func setupWindowIcon(title string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	findWindow := user32.NewProc("FindWindowW")
	sendMessage := user32.NewProc("SendMessageW")
	extractIcon := shell32.NewProc("ExtractIconW")

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	var hwnd uintptr
	// 窗口异步创建: 轮询最多 5 秒
	for i := 0; i < 25 && hwnd == 0; i++ {
		hwnd, _, _ = findWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if hwnd == 0 {
		return
	}
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exePtr, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return
	}
	// ExtractIconW 返回 1/0 表示无图标(1 也是错误值)
	hIcon, _, _ := extractIcon.Call(0, uintptr(unsafe.Pointer(exePtr)), 0)
	if hIcon == 0 || hIcon == 1 {
		return
	}
	const wmSetIcon = 0x0080
	sendMessage.Call(hwnd, wmSetIcon, 0, hIcon) // ICON_SMALL: 标题栏
	sendMessage.Call(hwnd, wmSetIcon, 1, hIcon) // ICON_BIG: Alt-Tab
}
