package ui

import (
	"os"
	"path/filepath"

	"github.com/lxn/walk"

	"wallpaper/internal/config"
	"wallpaper/internal/install"
)

// RunUninstall 命令行 --uninstall 入口(Windows 设置 > 应用 调用): 弹确认框后执行卸载。
func RunUninstall() int {
	proceed, removeData := confirmUninstall(nil)
	if !proceed {
		return 0
	}
	doUninstall(config.DefaultDir(), removeData)
	return 0
}

// doUninstall 执行卸载并立即退出进程(以便分离助手能删除运行中的 exe)。
func doUninstall(dir string, removeData bool) {
	binPath := install.BinPath(filepath.Join(dir, "bin"))
	_ = install.Uninstall(binPath, dir, removeData)
	os.Exit(0)
}

// confirmUninstall 卸载确认框: 默认保留偏好与壁纸, 可选连同数据一并删除。
// 返回 (是否继续, 是否删除全部数据)。
func confirmUninstall(owner walk.Form) (proceed, removeData bool) {
	dlg, err := walk.NewDialog(owner)
	if err != nil {
		return false, false
	}
	initTheme()
	dlg.SetTitle(config.AppNameCN + " — 卸载")
	if icon, err := walk.NewIconFromResourceId(2); err == nil {
		_ = dlg.SetIcon(icon)
	}
	dlg.SetBackground(brushPageBg)
	dlg.SetSize(walk.Size{Width: 460, Height: 300})
	_ = dlg.SetLayout(walk.NewVBoxLayout())

	title, _ := walk.NewLabel(dlg)
	title.SetText("卸载 " + config.AppNameCN)
	title.SetFont(themeFontStep)

	desc, _ := walk.NewLabel(dlg)
	desc.SetText("将停止每小时自动换图, 并移除桌面/开始菜单快捷方式与卸载项。\n请选择如何处理你的数据:")

	keepRb, _ := walk.NewRadioButton(dlg)
	keepRb.SetText("保留我的偏好与壁纸(推荐; 日后重装可直接沿用)")
	delRb, _ := walk.NewRadioButton(dlg)
	delRb.SetText("连同偏好、壁纸、日志一并删除(彻底清理)")
	keepRb.SetChecked(true)

	btnRow, _ := walk.NewComposite(dlg)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	hsp, _ := walk.NewHSpacer(btnRow)
	cancelBtn, _ := walk.NewPushButton(btnRow)
	cancelBtn.SetText("取消")
	cancelBtn.Clicked().Attach(func() { dlg.Cancel() })
	okBtn, _ := walk.NewPushButton(btnRow)
	okBtn.SetText("确认卸载")
	okBtn.Clicked().Attach(func() {
		proceed = true
		removeData = delRb.Checked()
		dlg.Accept()
	})
	if bl, ok := btnRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}
	dlg.Run()
	return proceed, removeData
}
