package scheduler

import (
	"os/exec"
	"syscall"
)

// hideWindow 让 schtasks 子进程不闪黑框(GUI 子系统程序调控制台命令时需要)。
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
