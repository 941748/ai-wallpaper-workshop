package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"wallpaper/internal/scheduler"
)

// Uninstall 执行卸载(不阻塞): 删计划任务 → 删快捷方式 → 删 HKCU 卸载项 →
// 拉起分离的临时助手清理 exe/数据文件(运行中的 exe 无法自删, 由助手在本进程退出后处理)。
// removeData=true 时连同整个数据目录(偏好/壁纸/日志)一并删除; false 时仅删程序文件, 保留偏好与壁纸。
// 调用方应在本函数返回后立即退出进程, 以便助手能删除 exe。
func Uninstall(binPath, dataDir string, removeData bool) error {
	_ = scheduler.Delete()
	RemoveShortcuts()
	RemoveUninstallKey()
	return SpawnCleanupHelper(binPath, dataDir, removeData)
}

// SpawnCleanupHelper 把已安装 exe 复制到临时目录并以分离进程拉起, 参数为 --cleanup。
// 临时助手不持有原 exe 的文件锁, 因而能在原进程退出后删除安装文件, 最后自删(重启时清理)。
func SpawnCleanupHelper(binPath, dataDir string, removeData bool) error {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("aiwallpaper_uninstall_%d.exe", os.Getpid()))
	if err := CopyFile(binPath, tmp); err != nil {
		return err
	}
	flag := "0"
	if removeData {
		flag = "1"
	}
	cmd := exec.Command(tmp, "--cleanup",
		"--bin", binPath,
		"--dir", dataDir,
		"--removedata", flag)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x08000000, // DETACHED_PROCESS | CREATE_NO_WINDOW
	}
	return cmd.Start()
}

// Cleanup 由临时助手进程执行: 等待原进程退出释放 exe → 删除程序文件(及可选整目录) → 标记自删。
func Cleanup(binPath, dataDir string, removeData bool) {
	waitForRelease(binPath, 30*time.Second)
	if removeData && dataDir != "" {
		_ = os.RemoveAll(dataDir)
	} else {
		// 保留偏好与壁纸: 仅删 bin 目录(程序文件); 顺带清理历史安装名
		_ = os.RemoveAll(filepath.Dir(binPath))
		_ = os.Remove(LegacyBinPath(dataDir))
	}
	selfDeleteOnReboot()
}

// waitForRelease 轮询尝试删除 binPath, 直到成功(原进程已退出)或超时。
func waitForRelease(binPath string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(binPath); os.IsNotExist(err) {
			return // 已不存在
		}
		if err := os.Remove(binPath); err == nil {
			return // 删除成功(锁已释放)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

var (
	kernel32ProcMoveFileEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")
	movefileDelayUntilReboot = 0x4
)

// selfDeleteOnReboot 标记临时助手自身在下次重启时删除(运行中的 exe 无法即时自删)。
func selfDeleteOnReboot() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	p, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return
	}
	_, _, _ = kernel32ProcMoveFileEx.Call(
		uintptr(unsafe.Pointer(p)), 0, uintptr(movefileDelayUntilReboot))
}

// ParseBoolFlag 解析 "0"/"1" 形式的命令行布尔参数。
func ParseBoolFlag(s string) bool {
	b, _ := strconv.ParseBool(s)
	if b {
		return true
	}
	return s == "1"
}
