// Package ui 入口: 手动运行 exe = 向导或设置面板; 关闭窗口即进程退出。
package ui

import (
	"wallpaper/internal/config"
	"wallpaper/internal/store"
)

// Run 启动 GUI(无参数运行)。返回进程退出码。
func Run() int {
	// 单实例: 已有向导/设置窗口在运行时直接激活它, 避免多开
	if !acquireSingleInstance() {
		activateExistingWindow()
		return 0
	}
	dir := config.DefaultDir()
	st, err := store.Open(dir)
	if err != nil {
		showError(nil, "数据目录创建失败: %v", err)
		return 1
	}
	cfg, err := config.Load(dir)
	if err != nil {
		showError(nil, "配置读取失败: %v", err)
		return 1
	}
	if !cfg.Initialized {
		RunWizard(dir, st, cfg)
		return 0
	}
	RunSettings(dir, st, cfg)
	return 0
}
