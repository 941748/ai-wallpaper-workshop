// Package install 负责客户端在本机的"安装"落地:
// 统一安装名(bin\AIWallpaper.exe)、计划任务注册、桌面/开始菜单快捷方式、
// HKCU 卸载注册表项, 以及自卸载(默认保留偏好与壁纸, 可选全删)。
//
// 设计红线: 无独立安装器 —— 用户双击 exe 即"安装"(复制到数据目录 bin\)。
// 所有写入均落在当前用户可写位置(%LOCALAPPDATA% / HKCU / 用户桌面与开始菜单), 不需要管理员权限。
package install

import (
	"os"
	"path/filepath"

	"wallpaper/internal/config"
	"wallpaper/internal/scheduler"
)

// ExeName 统一安装文件名(计划任务、快捷方式、卸载项、自更新均指向此名)。
const ExeName = "AIWallpaper.exe"

// legacyExeName 历史安装文件名(v1.0.4 及以前); 迁移时清理。
const legacyExeName = "wallpaper.exe"

// DisplayName 展示名(快捷方式/卸载项)。
const DisplayName = config.AppNameCN

// uninstallRegKey HKCU 卸载注册表项路径(Windows 设置 > 应用可见)。
const uninstallRegKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\AIWallpaper`

// BinPath 返回统一安装路径 bin\AIWallpaper.exe。
func BinPath(binDir string) string { return filepath.Join(binDir, ExeName) }

// LegacyBinPath 返回历史安装路径 bin\wallpaper.exe。
func LegacyBinPath(binDir string) string { return filepath.Join(binDir, legacyExeName) }

// CopyFile 复制文件(用于把运行中的 exe 落地到安装目录)。
func CopyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// Setup 完成一次"安装"(幂等): 复制 exe 到 bin\AIWallpaper.exe → 清理历史名 →
// 注册每小时计划任务 → 建桌面/开始菜单快捷方式 → 写 HKCU 卸载项。
// selfExe 为当前运行的 exe 路径(可能是下载目录的原始文件, 也可能是已安装的副本)。
// 返回最终安装路径。任务节律固定每小时(换图频率由 tick 内部节流), 与 tick 保持一致。
func Setup(binDir, selfExe, version string, phaseMinutes int) (string, error) {
	dst := BinPath(binDir)
	if !samePath(selfExe, dst) {
		if err := CopyFile(selfExe, dst); err != nil {
			return "", err
		}
	}
	// 清理历史安装名(运行中的旧 exe 无法删除, 忽略错误, 由 tick 迁移收尾)
	if legacy := LegacyBinPath(binDir); !samePath(legacy, dst) {
		_ = os.Remove(legacy)
	}
	if err := scheduler.Register(dst, scheduler.TickHours, phaseMinutes); err != nil {
		return dst, err
	}
	// 快捷方式与卸载项失败不阻断安装(非致命)
	_ = CreateShortcuts(dst)
	_ = WriteUninstallKey(dst, filepath.Dir(binDir), version)
	return dst, nil
}

// samePath 判断两路径是否指向同一文件(大小写不敏感, Windows 语义)。
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b) ||
		equalFoldClean(a, b)
}

func equalFoldClean(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := 0; i < len(ca); i++ {
		x, y := ca[i], cb[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
