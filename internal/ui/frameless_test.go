package ui

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestFrameCallbackSignature 确认无边框子类化用的窗口过程回调签名能被 windows.NewCallback 接受。
// 该回调混合了 uintptr / uint32 / unsafe.Pointer 参数(对应 HWND/UINT/WPARAM/LPARAM),
// 若签名不被接受, NewCallback 会 panic —— 窗口一打开就崩溃。此测试将其前移到 CI。
func TestFrameCallbackSignature(t *testing.T) {
	cb := windows.NewCallback(func(h uintptr, msg uint32, wp uintptr, lp unsafe.Pointer) uintptr {
		_, _, _, _ = h, msg, wp, lp
		return 0
	})
	if cb == 0 {
		t.Fatal("NewCallback 返回空指针")
	}
}
