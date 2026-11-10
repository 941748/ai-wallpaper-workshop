// Package ui 入口: 手动运行 exe = Wails GUI(设置面板, 前端路由); 关窗即进程退出。
// walk 版页面在 P2-c 迁移完成前暂时保留编译, 最终整体删除(见 docs/wails-migration.md)。
package ui

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"wallpaper/internal/config"
	"wallpaper/internal/store"
)

// webview2Messages WebView2 缺失/升级时的系统对话框文案(默认 download 策略:
// 弹确认框 → 下载安装 → 继续)。Wails 默认英文, 面向国内用户中文化。
func webview2Messages() *windows.Messages {
	return &windows.Messages{
		InstallationRequired: "本程序需要 WebView2 运行时才能显示界面。点击「确定」自动下载安装(静默安装, 请稍候); 取消将退出程序。",
		UpdateRequired:       "WebView2 运行时版本过旧, 需要更新。点击「确定」自动下载安装(静默安装, 请稍候); 取消将退出程序。",
		MissingRequirements:  "缺少运行条件",
		Webview2NotInstalled: "未安装 WebView2 运行时, 程序无法启动。",
		Error:                "错误",
		FailedToInstall:      "WebView2 运行时安装失败, 请检查网络后重试; 或手动安装 Microsoft Edge WebView2 运行时。",
		ContactAdmin:         "本程序需要 WebView2 运行时, 请联系系统管理员安装。",
	}
}

// Run 启动 GUI(无参数运行)。返回进程退出码。
func Run(assets embed.FS) int {
	// 单实例: 已有窗口时激活它, 避免多开
	if !acquireSingleInstance() {
		activateExistingWindow()
		return 0
	}
	dir := config.DefaultDir()
	st, err := store.Open(dir)
	if err != nil {
		showError(nil, "数据目录创建失败: %v", friendlyErrText(err))
		return 1
	}
	cfg, err := config.Load(dir)
	if err != nil {
		showError(nil, "配置读取失败: %v", friendlyErrText(err))
		return 1
	}

	bridge := &Bridge{dir: dir, st: st, cfg: cfg}
	bridge.be, bridge.cc = makeBackend(cfg) // 出图链路与云客户端(历史/出图/配对均依赖, 忘初始化会全报直连模式)
	err = wails.Run(&options.App{
		Title:     config.AppNameCN,
		Width:     1180,
		Height:    760,
		MinWidth:  960,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 17, B: 23, A: 1},
		Windows: &windows.Options{
			Messages: webview2Messages(),
		},
		OnStartup:        bridge.startup,
		Bind: []interface{}{
			bridge,
		},
	})
	if err != nil {
		showError(nil, "窗口启动失败: %v", friendlyErrText(err))
		return 1
	}
	return 0
}
