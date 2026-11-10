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
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/image/draw"

	"wallpaper/internal/backend"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/install"
	"wallpaper/internal/scheduler"
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
}

// BootInfo 前启时拉取的引导信息。
type BootInfo struct {
	Version string `json:"version"`
	Dir     string `json:"dir"`
	UserID  string `json:"userId"`
}

func (b *Bridge) startup(ctx context.Context) {
	b.ctx = ctx
	// 标题栏图标: Wails 按资源ID3加载而 rsrc 图标组实际在ID9(见 windowicon.go), 运行时兜底
	go setupWindowIcon(config.AppNameCN)
	// 无向导(负责人 2026-11-09: 初始化无用户决策点): 首装的注册/默认包/计划任务
	// 全部后台静默完成, 窗口直接进设置页
	go b.FirstRunSetup()
}

// Boot 返回引导信息(验证 Go↔JS 通道; 前端读版本/数据目录)。
func (b *Bridge) Boot() BootInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BootInfo{
		Version: b.version(),
		Dir:     b.dir,
		UserID:  b.cfg.UserID,
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

// ---------- 首装静默初始化(向导已移除, 负责人 2026-11-09) ----------

// FirstRunSetup 首次启动后台初始化(幂等, startup 异步调用):
//  1. 匿名注册(无身份时; 失败不阻塞, 云端方法有 ensureIdentity 兜底重试)
//  2. 全新画像默认落治愈日常风格包(负责人: 直接默认, 无需用户决定)
//  3. 静默安装(计划任务/快捷方式/卸载项; 仅发行文件名 AIWallpaper.exe 才写系统级
//     副作用, 防开发/测试进程劫持计划任务——见 go-test 0x80070002 记忆)
//  4. 置 Initialized(安装失败则保持 false → 下次启动自愈重试)
func (b *Bridge) FirstRunSetup() {
	b.mu.Lock()
	if b.cfg.Initialized {
		b.mu.Unlock()
		return
	}
	hasID := b.cfg.UserID != "" && b.cfg.Token != ""
	b.mu.Unlock()

	// 1) 匿名注册(8s 上限, 失败留待 ensureIdentity 重试)
	if !hasID {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		b.mu.Lock()
		err := ensureRegistered(ctx, b.cfg)
		if err == nil {
			_ = b.cfg.Save(b.dir)
		}
		b.mu.Unlock()
		cancel()
		if err != nil {
			b.st.Log("FirstRunSetup: 注册失败(稍后云端操作时重试): %v", err)
		}
	}

	// 2) 全新画像 → 显式落默认风格包(引擎按 Packs 执行, 不再依赖推断)
	profile, _ := config.LoadProfile(b.dir)
	if profile != nil && profile.Packs == nil && likesInProfile(profile) == 0 {
		profile.Packs = selectedPackNames(initSelectedPacks(profile))
		if err := profile.Save(b.dir); err != nil {
			b.st.Log("FirstRunSetup: 默认风格包落盘失败: %v", err)
		}
	}

	// 3) 静默安装(仅发行文件名; 保持 walk 版 FinishWizard 同语义)
	exe, err := exePath()
	if err != nil {
		b.st.Log("FirstRunSetup: 定位程序路径失败: %v", err)
		return
	}
	b.mu.Lock()
	b.cfg.PhaseMinutes = phaseFromUser(b.cfg.UserID)
	phase := b.cfg.PhaseMinutes
	b.mu.Unlock()
	installed := false
	if filepath.Base(exe) == "AIWallpaper.exe" {
		if _, err := install.Setup(b.st.BinDir(), exe, update.CurrentVersion, phase); err != nil {
			b.st.Log("FirstRunSetup: 安装/任务注册失败(下次启动重试): %v", err)
		} else {
			installed = true
		}
	} else {
		installed = true // 开发/测试构建: 跳过系统级副作用视为完成
	}

	// 4) 安装确认后置 Initialized(失败保持 false → 下次自愈重试)
	if installed {
		b.mu.Lock()
		b.cfg.Initialized = true
		b.cfg.TaskTickHours = scheduler.TickHours
		b.cfg.LastVersion = update.CurrentVersion
		if b.cfg.LastUpdate == "" {
			b.cfg.LastUpdate = time.Now().Format(time.RFC3339)
		}
		_ = b.cfg.Save(b.dir)
		b.be, b.cc = makeBackend(b.cfg)
		b.mu.Unlock()
		b.st.Log("FirstRunSetup: 首装初始化完成(计划任务/快捷方式就位)")
	}
}

// ensureIdentity 云端操作前的身份兜底: 无身份时补注册(幂等, 最多重试一次;
// 首装离线场景下各云端方法据此自愈)。持锁期间不做网络调用。
func (b *Bridge) ensureIdentity() {
	b.mu.Lock()
	if b.cfg.UserID != "" && b.cfg.Token != "" {
		b.mu.Unlock()
		return
	}
	cfg := b.cfg
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.UserID != "" && b.cfg.Token != "" {
		return
	}
	if err := ensureRegistered(ctx, cfg); err != nil {
		b.st.Log("ensureIdentity: 注册失败: %v", err)
		return
	}
	_ = cfg.Save(b.dir)
	b.be, b.cc = makeBackend(cfg)
}
