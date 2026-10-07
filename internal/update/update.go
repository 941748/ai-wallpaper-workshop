// Package update 客户端静默自更新。
// 链路: tick 尾部查云端 /client/latest → 下载 + sha256 校验 → 启动助手进程(同 exe)
// 等待 tick 进程退出 → 原子换装(旧版改名 .old → 新版上位) → 下次 tick 生效。
// 失败策略: 任一步失败保留旧版、记日志、下轮重试; 绝不中断换壁纸主线。
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wallpaper/internal/cloud"
)

// CurrentVersion 当前客户端版本(每次发版递增, 与 Release tag 对齐)。
const CurrentVersion = "1.0.1"

// CompareVersions 比较语义化版本: a<b 返回 -1, a==b 返回 0, a>b 返回 1。
func CompareVersions(a, b string) int {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool {
			return r < '0' || r > '9'
		}))
		out[i] = n
	}
	return out
}

// Check 检查并暂存新版本; 有更新且暂存成功时返回 true。
// 换装由助手进程完成(tick 结束时本进程即将退出)。
func Check(ctx context.Context, c *cloud.Client, dir string, exePath string, log func(string, ...any)) bool {
	latest, err := c.ClientLatest(ctx, CurrentVersion)
	if err != nil {
		log("自更新: 查询最新版本失败: %v", err)
		return false
	}
	if latest.Version == "" || CompareVersions(latest.Version, CurrentVersion) <= 0 {
		return false
	}
	log("自更新: 发现新版本 %s (当前 %s)", latest.Version, CurrentVersion)

	data, err := c.DownloadFile(ctx, latest.URL)
	if err != nil {
		log("自更新: 下载失败: %v", err)
		return false
	}
	if latest.Size > 0 && int64(len(data)) != latest.Size {
		log("自更新: 大小不符(期望 %d, 实际 %d), 放弃", latest.Size, len(data))
		return false
	}
	if latest.SHA256 != "" {
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), latest.SHA256) {
			log("自更新: sha256 校验失败, 放弃")
			return false
		}
	}
	newPath := filepath.Join(dir, "bin", "wallpaper.new.exe")
	if err := os.WriteFile(newPath, data, 0o755); err != nil {
		log("自更新: 写入新版本失败: %v", err)
		return false
	}
	if err := spawnHelper(exePath, exePath, newPath); err != nil {
		log("自更新: 启动换装助手失败: %v", err)
		return false
	}
	log("自更新: 新版本 %s 已暂存, 助手进程将在本进程退出后完成换装", latest.Version)
	return true
}

func spawnHelper(selfExe, target, newFile string) error {
	cmd := exec.Command(selfExe, "--apply-update", "--target", target, "--new", newFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x08000000, // DETACHED_PROCESS | CREATE_NO_WINDOW
	}
	return cmd.Start()
}

// ApplyUpdate 助手模式: 等待旧进程退出并原子换装(重试最多 120 秒)。
func ApplyUpdate(target, newFile string) error {
	deadline := time.Now().Add(120 * time.Second)
	oldPath := target + ".old"
	var renamed bool
	for time.Now().Before(deadline) {
		if err := os.Rename(target, oldPath); err == nil {
			renamed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !renamed {
		return fmt.Errorf("换装超时: 无法重命名 %s(旧进程可能仍在运行)", target)
	}
	if err := os.Rename(newFile, target); err != nil {
		// 回滚, 保留旧版可运行
		_ = os.Rename(oldPath, target)
		return fmt.Errorf("换装失败: %w", err)
	}
	_ = os.Remove(oldPath) // 旧进程未退出时删除失败, 由下次 tick 清理
	return nil
}

// CleanupOldFiles 清理上次换装残留(bin\*.old), 由 tick 开头调用。
func CleanupOldFiles(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "bin", "*.old"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
