// AI 壁纸工坊 — 单文件 Windows exe(Go, GUI 子系统)。
//
// 运行形态:
//
//	无参数          用户手动运行 = 向导(首次) / 设置面板(之后); 关窗即进程退出
//	--tick          计划任务每小时触发: 无窗口静默出图换壁纸后退出
//	--apply-update  内部换装助手: 等待旧进程退出后原子替换 exe(静默自更新)
//	--uninstall     卸载: 弹确认框, 删计划任务/快捷方式/注册表项后拉起清理助手并退出
//	--cleanup       内部卸载助手: 等待原进程退出后删除 exe(及可选整数据目录)
//
// 设计红线: 无托盘 / 无后台服务 / 无 Web 界面 / 无常驻进程。
package main

import (
	"context"
	"flag"
	"os"

	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/install"
	"wallpaper/internal/store"
	"wallpaper/internal/tick"
	"wallpaper/internal/ui"
	"wallpaper/internal/update"
)

func main() {
	tickMode := flag.Bool("tick", false, "计划任务单次运行(无窗口静默)")
	applyUpdate := flag.Bool("apply-update", false, "内部: 换装助手模式")
	target := flag.String("target", "", "内部: 换装目标路径")
	newFile := flag.String("new", "", "内部: 新版本文件路径")
	uninstall := flag.Bool("uninstall", false, "卸载本程序(弹确认框)")
	cleanup := flag.Bool("cleanup", false, "内部: 卸载清理助手模式")
	cleanBin := flag.String("bin", "", "内部: 待清理的已安装 exe 路径")
	cleanDir := flag.String("dir", "", "内部: 数据目录")
	cleanRemoveData := flag.String("removedata", "0", "内部: 1=连同数据目录一并删除")
	flag.Parse()

	if *cleanup {
		install.Cleanup(*cleanBin, *cleanDir, install.ParseBoolFlag(*cleanRemoveData))
		return
	}

	if *applyUpdate && *target != "" && *newFile != "" {
		_ = update.ApplyUpdate(*target, *newFile)
		return
	}

	if *uninstall {
		os.Exit(ui.RunUninstall())
		return
	}

	if *tickMode {
		runTick()
		return
	}

	os.Exit(ui.Run())
}

// runTick 计划任务单次流程: 全程静默, 任何失败均保留旧壁纸。
func runTick() {
	desktop.SetLowPriority() // BelowNormal: 不占用用户资源

	dir := config.DefaultDir()
	st, err := store.Open(dir)
	if err != nil {
		return
	}
	env := tick.Env{
		Dir: dir,
		Log: func(format string, args ...any) { st.Log(format, args...) },
		Ask: func(req tick.AskRequest) tick.AskChoice {
			return ui.ShowAskDialog(req) // 到期回访: tick 进程内弹小窗, 关闭后即退
		},
		SelfUpdate: true,
	}
	_ = tick.RunOnce(context.Background(), env)
}
