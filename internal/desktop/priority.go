package desktop

import "golang.org/x/sys/windows"

// SetLowPriority 将当前进程优先级设为 BelowNormal(tick 轻量进程, 不占用用户资源)。
func SetLowPriority() {
	if h, err := windows.GetCurrentProcess(); err == nil {
		_ = windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS)
	}
}
