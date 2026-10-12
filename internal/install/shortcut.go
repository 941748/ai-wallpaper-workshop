package install

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件用 IShellLink COM 接口创建/删除 .lnk 快捷方式, 不引入任何第三方库。
// 说明: 直接用 syscall 调用 COM 虚表, 索引遵循 IShellLinkW / IPersistFile 的方法顺序。

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	shell32                  = windows.NewLazySystemDLL("shell32.dll")
	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
)

// guid COM 接口/类标识(与 C 语言 GUID 内存布局一致)。
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidShellLink  = guid{0x00021401, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIShellLinkW  = guid{0x000214F9, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIPersistFile = guid{0x0000010B, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}

	// FOLDERID_Desktop / FOLDERID_Programs(开始菜单\程序)
	folderDesktop  = guid{0xB4BFCC3A, 0xDB2C, 0x424C, [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	folderPrograms = guid{0xA77F5D77, 0x2E2B, 0x44C3, [8]byte{0xA6, 0xA2, 0xAB, 0xA6, 0x01, 0x05, 0x4A, 0x51}}
)

// IShellLinkW 虚表方法索引。
const (
	vtRelease         = 2
	vtQueryInterface  = 0
	vtSetDescription  = 7
	vtSetWorkingDir   = 9
	vtSetArguments    = 11
	vtSetIconLocation = 17
	vtSetPath         = 20
	vtPfSave          = 6 // IPersistFile::Save
)

const coinitApartmentThreaded = 0x2

// vtableOf 取 COM 对象虚表(函数指针数组)。
// obj 始终以 unsafe.Pointer 承载(不做 uintptr→Pointer 转换, 避免 go vet unsafeptr 误报):
// COM 对象首部是指向虚表的指针, 解引用得到虚表, 再视为函数指针数组。
func vtableOf(obj unsafe.Pointer) []uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	return (*[32]uintptr)(vtbl)[:]
}

// knownFolder 通过 SHGetKnownFolderPath 取用户已知文件夹真实路径(支持 OneDrive 重定向)。
func knownFolder(id *guid) (string, error) {
	var p *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(
		uintptr(unsafe.Pointer(id)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if hr != 0 || p == nil {
		return "", fmt.Errorf("SHGetKnownFolderPath: 0x%x", hr)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return windows.UTF16PtrToString(p), nil
}

// shortcutPath 返回某已知文件夹下的快捷方式完整路径。
func shortcutPath(folder *guid) (string, error) {
	dir, err := knownFolder(folder)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DisplayName+".lnk"), nil
}

// CreateShortcuts 在桌面与开始菜单\程序各创建一个指向 targetExe 的快捷方式。
// 任一失败返回错误(但不致命, 调用方可忽略)。
func CreateShortcuts(targetExe string) error {
	desktop, err := shortcutPath(&folderDesktop)
	if err != nil {
		return err
	}
	programs, err := shortcutPath(&folderPrograms)
	if err != nil {
		return err
	}
	if err := createShortcut(desktop, targetExe); err != nil {
		return err
	}
	return createShortcut(programs, targetExe)
}

// RemoveShortcuts 删除桌面与开始菜单的快捷方式(不存在则忽略)。
func RemoveShortcuts() {
	if p, err := shortcutPath(&folderDesktop); err == nil {
		_ = os.Remove(p)
	}
	if p, err := shortcutPath(&folderPrograms); err == nil {
		_ = os.Remove(p)
	}
}

// createShortcut 用 IShellLinkW + IPersistFile 写一个 .lnk。
func createShortcut(lnkPath, target string) error {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_OK(0)/S_FALSE(1)/RPC_E_CHANGED_MODE(0x80010106) 均可继续; 其余视为失败
	if hr != 0 && hr != 1 && hr != 0x80010106 {
		return fmt.Errorf("CoInitializeEx: 0x%x", hr)
	}
	defer procCoUninitialize.Call()

	var link unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, 0x1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidIShellLinkW)),
		uintptr(unsafe.Pointer(&link)))
	if hr != 0 || link == nil {
		return fmt.Errorf("CoCreateInstance(IShellLink): 0x%x", hr)
	}
	vt := vtableOf(link)
	defer syscall.SyscallN(vt[vtRelease], uintptr(link))

	if err := setPath(vt, link, target); err != nil {
		return err
	}
	// 工作目录 = exe 所在目录
	_ = setStr(vt[vtSetWorkingDir], link, filepath.Dir(target))
	_ = setStr(vt[vtSetDescription], link, DisplayName)
	// 图标: 复用 exe 内嵌图标(索引 0)
	if p, err := syscall.UTF16PtrFromString(target); err == nil {
		syscall.SyscallN(vt[vtSetIconLocation], uintptr(link), uintptr(unsafe.Pointer(p)), 0)
	}

	// QueryInterface → IPersistFile, 调 Save 落盘
	var pf unsafe.Pointer
	if hr, _, _ = syscall.SyscallN(vt[vtQueryInterface], uintptr(link),
		uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))); hr != 0 || pf == nil {
		return fmt.Errorf("QueryInterface(IPersistFile): 0x%x", hr)
	}
	pvt := vtableOf(pf)
	defer syscall.SyscallN(pvt[vtRelease], uintptr(pf))

	plnk, err := syscall.UTF16PtrFromString(lnkPath)
	if err != nil {
		return err
	}
	if hr, _, _ = syscall.SyscallN(pvt[vtPfSave], uintptr(pf), uintptr(unsafe.Pointer(plnk)), 1); hr != 0 {
		return fmt.Errorf("IPersistFile.Save(%s): 0x%x", lnkPath, hr)
	}
	return nil
}

func setPath(vt []uintptr, link unsafe.Pointer, target string) error {
	p, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if hr, _, _ := syscall.SyscallN(vt[vtSetPath], uintptr(link), uintptr(unsafe.Pointer(p))); hr != 0 {
		return fmt.Errorf("IShellLink.SetPath: 0x%x", hr)
	}
	return nil
}

func setStr(fn uintptr, link unsafe.Pointer, s string) error {
	if s == "" {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	if hr, _, _ := syscall.SyscallN(fn, uintptr(link), uintptr(unsafe.Pointer(p))); hr != 0 {
		return fmt.Errorf("setStr: 0x%x", hr)
	}
	return nil
}
