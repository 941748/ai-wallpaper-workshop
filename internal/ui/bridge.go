package ui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/image/draw"

	"wallpaper/internal/backend"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/store"
	"wallpaper/internal/update"
)

// Bridge 暴露给前端的 Go↔JS 绑定(方法需导出, 返回值 JSON 序列化)。
// Wails 从独立 goroutine 调用方法, 可变状态须经 mu 保护。
type Bridge struct {
	mu  sync.Mutex
	ctx context.Context
	dir string
	st  *store.Store
	cfg *config.Config

	// 出图链路(makeBackend 结果; 配置变更时重建)
	be backend.ImageBackend
	cc *cloud.Client
	// 连接/出图可用性(向导步骤2门禁)
	renderOK bool
}

// BootInfo 前启时拉取的引导信息。
type BootInfo struct {
	Version     string `json:"version"`
	Initialized bool   `json:"initialized"`
	Dir         string `json:"dir"`
	UserID      string `json:"userId"`
}

func (b *Bridge) startup(ctx context.Context) { b.ctx = ctx }

// Boot 返回引导信息(验证 Go↔JS 通道 + 前端决定首屏路由)。
func (b *Bridge) Boot() BootInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BootInfo{
		Version:     b.version(),
		Initialized: b.cfg.Initialized,
		Dir:         b.dir,
		UserID:      b.cfg.UserID,
	}
}

func (b *Bridge) version() string { return update.CurrentVersion }

// emit 前端事件广播(progress/fav/recall 等进度推送)。
func (b *Bridge) emit(event string, data any) {
	if b.ctx != nil {
		runtime.EventsEmit(b.ctx, event, data)
	}
}

// ---------- 通用操作 ----------

// OpenFolder 在资源管理器打开目录。
func (b *Bridge) OpenFolder(path string) {
	if path == "" {
		path = b.dir
	}
	_ = exec.Command("explorer", strings.ReplaceAll(path, "/", "\\")).Start()
}

// OpenURL 用系统浏览器打开 URL。
func (b *Bridge) OpenURL(url string) { openURL(url) }

// QuitApp 关闭应用(前端"完成"按钮用)。
func (b *Bridge) QuitApp() {
	if b.ctx != nil {
		runtime.Quit(b.ctx)
	}
}

// RestartWizard 置未初始化并要求前端重走向导(设置页"重新初始化"入口)。
func (b *Bridge) RestartWizard() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg.Initialized = false
	return b.cfg.Save(b.dir)
}

// ErrMsg 统一错误文案出口: 技术细节脱敏后再给前端展示。
func ErrMsg(err error) string {
	if err == nil {
		return ""
	}
	return friendlyErrText(err)
}

// textMsg 纯字符串错误的脱敏出口。
func textMsg(s string) string { return friendlyText(s) }

// scaleToB64 解码图片(自动识别 PNG/JPEG — 修复云端 JPEG 喂给 png.Decode 的预览 bug),
// 等比缩放到 maxW×maxH 内, 以 JPEG base64 返回(传输体积小)。
func scaleToB64(data []byte, maxW, maxH int) (string, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("图片解码失败: %w", err)
	}
	sb := src.Bounds()
	w, h := maxW, maxH
	if sb.Dx() > 0 && sb.Dy() > 0 {
		scale := float64(maxW) / float64(sb.Dx())
		if s := float64(maxH) / float64(sb.Dy()); s < scale {
			scale = s
		}
		w = int(float64(sb.Dx()) * scale)
		h = int(float64(sb.Dy()) * scale)
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 88}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// readFileB64 读文件并缩放为 JPEG base64 预览。
func readFileB64(path string, maxW, maxH int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return scaleToB64(data, maxW, maxH)
}

// exePath 当前可执行文件路径。
func exePath() (string, error) { return os.Executable() }

// trimSpace 去首尾空白。
func trimSpace(s string) string { return strings.TrimSpace(s) }

// saveNamed 按指定文件名留档(召回/新命名规则 {jobID}.ext), 返回完整路径。
func (b *Bridge) saveNamed(name string, data []byte) (string, error) {
	dir := b.st.WallpapersDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
