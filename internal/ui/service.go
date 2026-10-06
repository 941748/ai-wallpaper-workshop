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
	"wallpaper/internal/probe"
	"wallpaper/internal/prompt"
	"wallpaper/internal/taxonomy"
)

// 探针出图尺寸(固定 1024x768)。
const (
	probeW = 1024
	probeH = 768
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

// testConnection 连通性测试(含配额预检); 直连模式测 ComfyUI。
func testConnection(cfg *config.Config) error {
	if cfg.DirectMode && cfg.DirectURL != "" {
		c := comfyui.New(cfg.DirectURL, cfg.DirectToken)
		if c.BaseURL == "" {
			return fmt.Errorf("请填写 ComfyUI 地址")
		}
		return nil
	}
	c := cloud.New(cfg.CloudURL, cfg.UserID, cfg.Token, hostName())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	if !h.OK {
		return fmt.Errorf("云端未就绪")
	}
	if !h.QuotaOK {
		return fmt.Errorf("云端配额已用尽")
	}
	return nil
}

// acquireProbeImages 获取一组探针图: 优先云端公共池(秒开), 失败/不足则现场出图。
// progress 回调(done,total) 供 UI 刷新。
func acquireProbeImages(ctx context.Context, cfg *config.Config, cc *cloud.Client, be backend.ImageBackend,
	combos []probe.Combo, progress func(done, total int)) map[string][]byte {

	images := map[string][]byte{}
	total := len(combos)

	// 1) 云端公共探针池(仅首轮有池; 精炼轮组合个性化, 直接现场生成)
	if cc != nil && len(combos) > 0 && combos[0].Kind == probe.FirstRound {
		items, err := cc.ProbesSample(ctx, total)
		if err == nil {
			byID := map[string]cloud.ProbeItem{}
			for _, it := range items {
				byID[it.ID] = it
			}
			for i, cb := range combos {
				if it, ok := byID[cb.ID]; ok && it.URL != "" {
					if data, err := cc.ProbesImage(ctx, it.URL); err == nil && desktop.Inspect(data) == nil {
						images[cb.ID] = data
					}
				}
				progress(i+1, total)
			}
			if len(images) == total {
				return images
			}
		}
	}

	// 2) 现场生成兜底
	for i, cb := range combos {
		if _, ok := images[cb.ID]; ok {
			continue
		}
		data, err := generateCombo(ctx, be, cb)
		if err == nil && desktop.Inspect(data) == nil {
			images[cb.ID] = data
		}
		progress(i+1, total)
	}
	return images
}

// generateCombo 现场生成一张探针图(1024x768)。
func generateCombo(ctx context.Context, be backend.ImageBackend, cb probe.Combo) ([]byte, error) {
	id, err := be.Submit(ctx, backend.GenParams{
		Positive: cb.Prompt(),
		Negative: taxonomy.BaseNegative,
		Width:    probeW,
		Height:   probeH,
		Seed:     cb.Seed,
	})
	if err != nil {
		return nil, err
	}
	return be.Wait(ctx, id)
}

// generateWallpaper 生成首张/立即换一张壁纸(云端 LLM 优先, 本地引擎兜底), 返回适配屏幕后的 PNG 与提示词规格。
func generateWallpaper(ctx context.Context, cfg *config.Config, cc *cloud.Client, be backend.ImageBackend,
	profile *config.Profile, history []prompt.HistoryEntry, forceShake bool) ([]byte, prompt.Spec, error) {

	sw, sh := desktop.PrimarySize()
	var spec prompt.Spec
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
		return nil, spec, err
	}
	raw, err := be.Wait(ctx, id)
	if err != nil {
		return nil, spec, err
	}
	if err := desktop.Inspect(raw); err != nil {
		return nil, spec, fmt.Errorf("质量自检未通过: %w", err)
	}
	final, err := desktop.FitToScreen(raw, sw, sh)
	if err != nil {
		return nil, spec, err
	}
	return final, spec, nil
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
