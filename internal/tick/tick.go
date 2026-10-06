// Package tick 实现 --tick 单次运行的全流程编排:
// 文件锁 → 读配置 → 安静检查 → 满意度回访 → 云端健康/配额 → 取成品(预生成优先)
// → 出图(云端 LLM 定提示词+尺寸+工作流, 不可达回退本地引擎) → 适配 → 换壁纸
// → 预约下一轮预生成 → 静默自更新 → 上报(离线补传) → 留档清理 → 退出。
// 全程无窗口, 进程秒级~分钟级即退, 无常驻。
package tick

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"wallpaper/internal/backend"
	"wallpaper/internal/backend/comfyui"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/prompt"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
	"wallpaper/internal/update"
)

// AskChoice 满意度回访选择。
type AskChoice string

// 回访选择。
const (
	ChoiceSatisfied AskChoice = "satisfied" // 满意: 曲线上调一档(3→7→15→30→60→90 封顶)
	ChoiceStyle     AskChoice = "style"     // 换个风格: 立即换 + 强漂移 + 周期重置 3 天
	ChoiceLater     AskChoice = "later"     // 以后再说: 顺延 3 天(档位不变)
	ChoiceAdjust    AskChoice = "adjust"    // 调整一下: 已打开设置面板, 顺延 3 天
	ChoiceTimeout   AskChoice = "timeout"   // 无响应: 按沉默处理, 曲线也上调一档(不追问)
)

// AskRequest 回访小窗参数。
type AskRequest struct {
	ThumbPath  string // 当前壁纸缩略图(可为空)
	LastChoice string
}

// Env 运行环境(默认值见 withDefaults, 测试可注入)。
type Env struct {
	Dir        string
	Log        func(format string, args ...any)
	Now        func() time.Time
	Screen     func() (int, int)
	Quiet      func(cfg *config.Config, now time.Time) bool
	SetWP      func(path string) error
	Ask        func(AskRequest) AskChoice // nil = 不弹(顺延处理)
	ExePath    string
	SelfUpdate bool
}

func (e Env) withDefaults() Env {
	if e.Log == nil {
		e.Log = func(string, ...any) {}
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Screen == nil {
		e.Screen = desktop.PrimarySize
	}
	if e.Quiet == nil {
		e.Quiet = desktop.IsQuiet
	}
	if e.SetWP == nil {
		e.SetWP = desktop.SetWallpaper
	}
	if e.ExePath == "" {
		if p, err := os.Executable(); err == nil {
			e.ExePath = p
		}
	}
	return e
}

// RunOnce 执行一轮完整流程; 所有失败路径均静默保留旧壁纸, 绝不打扰用户。
func RunOnce(ctx context.Context, env Env) error {
	env = env.withDefaults()
	st, err := store.Open(env.Dir)
	if err != nil {
		return err
	}
	release, err := st.Lock()
	if errors.Is(err, store.ErrBusy) {
		env.Log("已有实例在运行, 退出")
		return nil
	}
	if err != nil {
		return err
	}
	defer release()

	update.CleanupOldFiles(env.Dir)

	cfg, err := config.Load(env.Dir)
	if err != nil {
		st.Log("tick: 配置读取失败: %v", err)
		return err
	}
	if !cfg.Initialized {
		env.Log("尚未初始化, 静默退出")
		return nil
	}

	now := env.Now()
	if env.Quiet(cfg, now) {
		st.Log("tick: 安静检测命中(锁屏/静默时段/全屏), 本轮跳过")
		return nil
	}

	// ---------- 满意度回访 ----------
	forceShake := handleSatisfaction(env, cfg, st, now)

	profile, err := config.LoadProfile(env.Dir)
	if err != nil {
		st.Log("tick: 画像读取失败: %v", err)
		profile = config.NewProfile()
	}
	history, _ := st.LoadHistory()

	// ---------- 云端 / 直连 ----------
	var (
		cc      *cloud.Client
		cloudOK bool
		be      backend.ImageBackend
	)
	if cfg.DirectMode && cfg.DirectURL != "" {
		be = comfyui.New(cfg.DirectURL, cfg.DirectToken)
		st.Log("tick: 直连调试模式 %s", cfg.DirectURL)
	} else {
		device, _ := os.Hostname()
		cc = cloud.New(cfg.CloudURL, cfg.UserID, cfg.Token, device)
		h, err := cc.Health(ctx)
		if err != nil {
			st.Log("tick: 云端不可达(%v), 保留当前壁纸, 下轮再试", err)
			env.Log("云端不可达: %v", err)
			return nil
		}
		if !h.QuotaOK {
			st.Log("tick: 云端配额用尽, 本轮跳过")
			return nil
		}
		cloudOK = true
		cc.FlushQueue(ctx, env.Dir) // 尽力补传历史离线记录
		be = &cloud.Backend{C: cc}
	}

	sw, sh := env.Screen()
	pending, _ := signals.Load(env.Dir)

	// ---------- 取本轮成品 ----------
	var (
		imgData  []byte
		spec     prompt.Spec
		source   = "local"
		wfID     string
		genW     = sw
		genH     = sh
		duration int64
	)
	genW, genH = chooseGenSize(sw, sh)

	// 预生成优先(换风格轮除外)
	if cloudOK && !forceShake {
		if p, err := cc.PregenFetch(ctx, cfg.ProfileVer); err == nil && p.Ready {
			if img, err := cc.PregenImage(ctx); err == nil && desktop.Inspect(img) == nil {
				imgData = img
				source = "pregen"
				wfID = p.WorkflowID
				spec = prompt.Spec{Combo: p.Combo, Positive: p.Positive, Negative: p.Negative, Seed: p.Seed}
				if p.Width > 0 {
					genW, genH = p.Width, p.Height
				}
				st.Log("tick: 命中云端预生成图")
			}
		}
	}

	if imgData == nil {
		// 提示词: 云端 LLM 优先, 失败回退本地引擎
		if cloudOK {
			nr, err := cc.NextPrompt(ctx, cloud.NextReq{
				ProfileVersion: cfg.ProfileVer,
				Profile:        profile.Weights,
				Disliked:       profile.Disliked,
				CustomKeywords: profile.CustomKeywords,
				RecentRounds:   recentRounds(history, 10),
				PendingSignals: pending,
				ScreenW:        sw,
				ScreenH:        sh,
				ForceShake:     forceShake,
				DisableContext: cfg.DisableContext,
			})
			if err == nil && nr.Positive != "" {
				spec = prompt.Spec{
					Combo: nr.Combo, Positive: nr.Positive, Negative: nr.Negative,
					Seed: nr.Seed, ShakeTurn: forceShake,
				}
				wfID = nr.WorkflowID
				source = "cloud"
				if nr.Width > 0 {
					genW, genH = nr.Width, nr.Height
				}
				st.Log("tick: 云端 LLM 提示词 (reason=%s)", nr.Reason)
			} else {
				if err != nil {
					st.Log("tick: 云端取提示词失败(%v), 回退本地引擎", err)
				}
				spec = localSpec(profile, history, now, forceShake)
			}
		} else {
			spec = localSpec(profile, history, now, forceShake)
		}

		params := backend.GenParams{
			Positive: spec.Positive, Negative: spec.Negative,
			Width: genW, Height: genH, Seed: spec.Seed, WorkflowID: wfID,
		}
		if forceShake {
			params.Priority = "normal"
		}
		start := env.Now()
		img, err := generateWithRetry(ctx, be, params)
		duration = env.Now().Sub(start).Milliseconds()
		if err != nil {
			st.Log("tick: 出图失败: %v, 保留当前壁纸", err)
			reportPrompt(env, cc, cfg, spec, genW, genH, wfID, source, duration, false)
			return nil
		}
		imgData = img
	}

	// ---------- 适配 + 换壁纸 ----------
	final, err := desktop.FitToScreen(imgData, sw, sh)
	if err != nil {
		st.Log("tick: 适配失败: %v, 保留当前壁纸", err)
		return nil
	}
	path, err := st.SaveWallpaper(final, now)
	if err != nil {
		st.Log("tick: 留档失败: %v", err)
		return nil
	}
	if err := env.SetWP(path); err != nil {
		st.Log("tick: 换壁纸失败: %v", err)
		return nil
	}
	st.SetCurrent(path)

	// ---------- 预约下一轮预生成 ----------
	if cloudOK {
		if err := cc.PregenSubmit(ctx, cloud.PregenReq{
			ProfileVersion: cfg.ProfileVer,
			Profile:        profile.Weights,
			Disliked:       profile.Disliked,
			CustomKeywords: profile.CustomKeywords,
			RecentRounds:   recentRounds(history, 10),
			ScreenW:        sw,
			ScreenH:        sh,
			DisableContext: cfg.DisableContext,
		}); err != nil {
			st.Log("tick: 预生成下单失败(忽略, 下轮现出): %v", err)
		}
	}

	// ---------- 静默自更新 ----------
	if cc != nil && env.SelfUpdate && env.ExePath != "" {
		update.Check(ctx, cc, env.Dir, env.ExePath, func(f string, a ...any) { st.Log("update: "+f, a...) })
	}

	// ---------- 上报 + 历史 ----------
	reportPrompt(env, cc, cfg, spec, genW, genH, wfID, source, duration, true)
	if cc != nil && len(pending) > 0 {
		if err := cc.ReportSignals(ctx, pending); err == nil {
			_ = signals.Clear(env.Dir)
		} else if err := cloud.Enqueue(env.Dir, "signals", pending); err == nil {
			_ = signals.Clear(env.Dir) // 已安全入补传队列(服务端按事件 ID 去重)
		}
	}

	_ = st.AppendHistory(prompt.HistoryEntry{
		At: now, Combo: spec.Combo, Positive: spec.Positive,
		Negative: spec.Negative, Seed: spec.Seed, Source: source,
	})

	_ = st.CleanupWallpapers(store.KeepWallpapers)
	st.Log("tick 完成: source=%s 屏幕=%dx%d 出图=%dx%d 耗时=%dms", source, sw, sh, genW, genH, duration)
	return nil
}

// ---------- 满意度回访 ----------

// handleSatisfaction 到期时弹回访小窗并应用选择; 返回是否"换个风格"轮。
func handleSatisfaction(env Env, cfg *config.Config, st *store.Store, now time.Time) bool {
	due := cfg.Satisfaction.NextAskTime()
	if due.IsZero() {
		cfg.Satisfaction.ScheduleAsk(now, "") // 首次: 从现在起算 3 天
		_ = cfg.Save(env.Dir)
		return false
	}
	if now.Before(due) {
		return false
	}
	choice := ChoiceTimeout // 无人应答(无 UI 通道)= 沉默: 曲线拉长不追问
	if env.Ask != nil {
		choice = env.Ask(AskRequest{ThumbPath: st.CurrentPath(), LastChoice: cfg.Satisfaction.LastChoice})
		switch choice {
		case ChoiceSatisfied, ChoiceStyle, ChoiceLater, ChoiceAdjust, ChoiceTimeout:
		default:
			choice = ChoiceLater
		}
	}
	cfg.Satisfaction.ScheduleAsk(now, string(choice))
	if err := cfg.Save(env.Dir); err != nil {
		st.Log("tick: 回访状态保存失败: %v", err)
	}
	switch choice {
	case ChoiceSatisfied:
		_ = signals.Append(env.Dir, signals.Event{Type: signals.TypeStyleKeep, Detail: "满意度回访: 满意"})
		st.Log("tick: 回访选择=满意, 曲线周期上调至 %d 天", cfg.Satisfaction.IntervalDays)
	case ChoiceStyle:
		_ = signals.Append(env.Dir, signals.Event{Type: signals.TypeStyleReject, Detail: "满意度回访: 换个风格"})
		st.Log("tick: 回访选择=换个风格, 本轮强制换风格, 周期重置 %d 天", cfg.Satisfaction.IntervalDays)
		return true
	case ChoiceAdjust:
		st.Log("tick: 回访选择=调整偏好, 设置窗口已打开, 顺延 3 天")
	case ChoiceTimeout:
		st.Log("tick: 回访无响应(沉默), 曲线周期上调至 %d 天", cfg.Satisfaction.IntervalDays)
	default:
		st.Log("tick: 回访选择=以后再说, 顺延 3 天")
	}
	return false
}

// ---------- 出图与提示词 ----------

func localSpec(profile *config.Profile, history []prompt.HistoryEntry, now time.Time, forceShake bool) prompt.Spec {
	rng := rand.New(rand.NewSource(now.UnixNano()))
	spec := prompt.Compose(profile, rng, prompt.Options{ForceShake: forceShake, History: history})
	spec.ShakeTurn = forceShake
	return spec
}

// generateWithRetry 出图 + 质量自检; 失败换种子重试一次。
func generateWithRetry(ctx context.Context, be backend.ImageBackend, params backend.GenParams) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		id, err := be.Submit(ctx, params)
		if err == nil {
			var img []byte
			img, err = be.Wait(ctx, id)
			if err == nil {
				if qerr := desktop.Inspect(img); qerr == nil {
					return img, nil
				} else {
					err = fmt.Errorf("质量自检未通过: %v", qerr)
				}
			}
		}
		lastErr = err
		params.Seed++ // 换种子重试
	}
	return nil, lastErr
}

func reportPrompt(env Env, cc *cloud.Client, cfg *config.Config, spec prompt.Spec, genW, genH int, wfID, source string, durationMs int64, ok bool) {
	if cc == nil {
		return
	}
	rec := cloud.PromptRecord{
		At: time.Now(), Positive: spec.Positive, Negative: spec.Negative,
		Combo: spec.Combo, Seed: spec.Seed, Width: genW, Height: genH,
		WorkflowID: wfID, Source: source, DurationMs: durationMs,
		Success: ok, ProfileVer: cfg.ProfileVer,
	}
	if err := cc.ReportPrompt(context.Background(), rec); err != nil {
		_ = cloud.Enqueue(env.Dir, "prompt", rec)
	}
}

// recentRounds 取最近 n 轮记录(供 LLM 参考)。
func recentRounds(history []prompt.HistoryEntry, n int) []cloud.RoundItem {
	if len(history) == 0 {
		return nil
	}
	start := len(history) - n
	if start < 0 {
		start = 0
	}
	out := make([]cloud.RoundItem, 0, n)
	for _, h := range history[start:] {
		out = append(out, cloud.RoundItem{At: h.At, Combo: h.Combo, Source: h.Source})
	}
	return out
}

// chooseGenSize 本地兜底出图尺寸: 固定 1920x1080(默认出图尺寸, 适配交给 FitToScreen)。
func chooseGenSize(sw, sh int) (int, int) {
	return 1920, 1080
}
