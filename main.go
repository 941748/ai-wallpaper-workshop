// AI 壁纸工坊 — 单文件 Windows exe(Go, GUI 子系统)。
//
// 运行形态:
//
//	无参数          用户手动运行 = 向导(首次) / 设置面板(之后); 关窗即进程退出
//	--tick          计划任务每小时触发: 无窗口静默出图换壁纸后退出
//	--apply-update  内部换装助手: 等待旧进程退出后原子替换 exe(静默自更新)
//
// 设计红线: 无托盘 / 无后台服务 / 无 Web 界面 / 无常驻进程。
package main

import (
	"context"
	"flag"
	"os"

	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
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
	flag.Parse()

	if *applyUpdate && *target != "" && *newFile != "" {
		_ = update.ApplyUpdate(*target, *newFile)
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
