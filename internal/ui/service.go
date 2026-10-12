package ui

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/backend"
	"wallpaper/internal/backend/comfyui"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/prompt"
)

// makeBackend 依据配置返回出图后端(cloud 默认 / comfyui-direct 调试)与云客户端(直连时 nil)。
func makeBackend(cfg *config.Config) (backend.ImageBackend, *cloud.Client) {
	if cfg.DirectMode && cfg.DirectURL != "" {
		return comfyui.New(cfg.DirectURL, cfg.DirectToken), nil
	}
	cc := cloud.New(cfg.CloudURL, cfg.UserID, cfg.Token, hostName())
	return &cloud.Backend{C: cc}, cc
}

// hostName 设备名(注册/统计用)。
func hostName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "windows-pc"
	}
	return h
}

// ensureRegistered 匿名注册(零注册零登录); 已有身份则跳过。
func ensureRegistered(ctx context.Context, cfg *config.Config) error {
	if cfg.UserID != "" && cfg.Token != "" {
		return nil
	}
	c := cloud.New(cfg.CloudURL, "", "", hostName())
	resp, err := c.Register(ctx, hostName())
	if err != nil {
		return fmt.Errorf("匿名注册失败: %w", err)
	}
	cfg.UserID = resp.UserID
	cfg.Token = resp.Token
	return nil
}

// testConnection 连通性测试: 云主站+配额由 err 表达, 并探测出图链路是否可用(renderOK)。
// 直连模式直接 ping ComfyUI; 云端模式读 /health 的 render_ok(服务端探测出图机)。
func testConnection(cfg *config.Config) (renderOK bool, err error) {
	if cfg.DirectMode {
		c := comfyui.New(cfg.DirectURL, cfg.DirectToken)
		if c.BaseURL == "" {
			return false, fmt.Errorf("请填写 ComfyUI 地址")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return c.Reachable(ctx), nil
	}
	c := cloud.New(cfg.CloudURL, cfg.UserID, cfg.Token, hostName())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := c.Health(ctx)
	if err != nil {
		return false, err
	}
	if !h.OK {
		return false, fmt.Errorf("云端未就绪")
	}
	if !h.QuotaOK {
		return false, fmt.Errorf("云端配额已用尽")
	}
	return h.RenderOK, nil
}

// generateWallpaper 生成首张/立即换一张壁纸(云端 LLM 优先, 本地引擎兜底), 返回适配屏幕后的 PNG、提示词规格与来源(cloud|local)。
func generateWallpaper(ctx context.Context, cfg *config.Config, cc *cloud.Client, be backend.ImageBackend,
	profile *config.Profile, history []prompt.HistoryEntry, forceShake bool) ([]byte, prompt.Spec, string, error) {

	sw, sh := desktop.PrimarySize()
	var spec prompt.Spec
	src := "local"
	genW, genH := fitGenSize(sw, sh)

	if cc != nil {
		nr, err := cc.NextPrompt(ctx, cloud.NextReq{
			ProfileVersion: cfg.ProfileVer,
			Profile:        profile.Weights,
			Disliked:       profile.Disliked,
			ScreenW:        sw,
			ScreenH:        sh,
			ForceShake:     forceShake,
		})
		if err == nil && nr.Positive != "" {
			spec = prompt.Spec{Positive: nr.Positive, Negative: nr.Negative, Seed: nr.Seed, Combo: nr.Combo}
			src = "cloud"
			if nr.Width > 0 {
				genW, genH = nr.Width, nr.Height
			}
		}
	}
	if spec.Positive == "" {
		rng := newRand()
		spec = prompt.Compose(profile, rng, prompt.Options{ForceShake: forceShake, History: history})
	}
	gp := backend.GenParams{
		Positive: spec.Positive, Negative: spec.Negative,
		Width: genW, Height: genH, Seed: spec.Seed,
	}
	id, err := be.Submit(ctx, gp)
	if err != nil {
		return nil, spec, src, err
	}
	raw, err := be.Wait(ctx, id)
	if err != nil {
		return nil, spec, src, err
	}
	if err := desktop.Inspect(raw); err != nil {
		return nil, spec, src, fmt.Errorf("质量自检未通过: %w", err)
	}
	final, err := desktop.FitToScreen(raw, sw, sh)
	if err != nil {
		return nil, spec, src, err
	}
	return final, spec, src, nil
}

// fitGenSize 本地兜底出图尺寸: 固定 1920x1080(默认出图尺寸, 适配交给 FitToScreen)。
func fitGenSize(sw, sh int) (int, int) {
	return 1920, 1080
}

func newRand() *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

// showError 弹出错误提示。
func showError(owner walk.Form, format string, args ...any) {
	walk.MsgBox(owner, config.AppNameCN, fmt.Sprintf(format, args...), walk.MsgBoxIconError)
}

// showInfo 弹出信息提示。
func showInfo(owner walk.Form, format string, args ...any) {
	walk.MsgBox(owner, config.AppNameCN, fmt.Sprintf(format, args...), walk.MsgBoxIconInformation)
}
