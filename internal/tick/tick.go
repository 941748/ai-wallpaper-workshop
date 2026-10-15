// Package tick 实现 --tick 单次运行的全流程编排:
// 文件锁 → 读配置 → 拉策略 → 节律判定 → 换图(本地备用池优先, 池空实时出图)
// → 补池与静默自更新并行执行 → 上报(离线补传) → 留档清理 → 退出。
// "静默"= 不换图但后台维护照常(补池/自更新/策略同步); 全程无窗口, 进程秒级~分钟级即退。
package tick

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wallpaper/internal/backend"
	"wallpaper/internal/backend/comfyui"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/install"
	"wallpaper/internal/policy"
	"wallpaper/internal/pool"
	"wallpaper/internal/prompt"
	"wallpaper/internal/scheduler"
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

// Outcome 单轮运行的结果反馈(设置页"立即换一张"经 Report 落盘后轮询读取)。
type Outcome struct {
	Changed bool   `json:"changed"`
	Detail  string `json:"detail"` // 未换图/失败原因(成功为空)
}

// OutcomePath 手动触发结果文件路径。
func OutcomePath(dir string) string {
	return filepath.Join(dir, "logs", "force_result.json")
}

// WriteOutcome 落盘手动触发结果。
func WriteOutcome(dir string, o Outcome) error {
	path := OutcomePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ReadOutcome 读取手动触发结果; 文件不存在或内容未写完整时返回错误。
func ReadOutcome(dir string) (Outcome, error) {
	var o Outcome
	b, err := os.ReadFile(OutcomePath(dir))
	if err != nil {
		return o, err
	}
	err = json.Unmarshal(b, &o)
	return o, err
}

// Env 运行环境(默认值见 withDefaults, 测试可注入)。
type Env struct {
	Dir        string
	Log        func(format string, args ...any)
	Now        func() time.Time
	Screen     func() (int, int)
	Quiet      func() bool // 锁屏/全屏等"不打扰"判定(换图节律由策略控制)
	SetWP      func(path string) error
	Ask        func(AskRequest) AskChoice // nil = 不弹(顺延处理)
	ExePath    string
	SelfUpdate bool
	Force      bool          // 手动触发(立即换一张): 绕过全部自动节律闸门
	Report     func(Outcome) // 单轮结果回调(覆盖所有提前退出路径)
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
	out := Outcome{}
	if env.Report != nil {
		defer func() { env.Report(out) }()
	}
	st, err := store.Open(env.Dir)
	if err != nil {
		out.Detail = "打开数据目录失败: " + err.Error()
		return err
	}
	release, err := st.Lock()
	if errors.Is(err, store.ErrBusy) {
		out.Detail = "上一轮换图仍在进行中, 请稍后再试"
		env.Log("已有实例在运行, 退出")
		return nil
	}
	if err != nil {
		out.Detail = "加锁失败: " + err.Error()
		return err
	}
	defer release()

	update.CleanupOldFiles(env.Dir)

	cfg, err := config.Load(env.Dir)
	if err != nil {
		out.Detail = "配置读取失败: " + err.Error()
		st.Log("tick: 配置读取失败: %v", err)
		return err
	}
	if !cfg.Initialized {
		out.Detail = "尚未初始化"
		env.Log("尚未初始化, 静默退出")
		return nil
	}

	now := env.Now()

	// 暂停到期自动恢复(防"关了忘了再开")
	if cfg.PauseUntil != "" {
		if t, err := time.Parse(time.RFC3339, cfg.PauseUntil); err == nil && now.After(t) {
			cfg.PauseUntil = ""
			if err := cfg.Save(env.Dir); err != nil {
				st.Log("tick: 暂停状态保存失败: %v", err)
			}
			st.Log("tick: 暂停到期, 已自动恢复换图")
		}
	}
	paused := cfg.PauseUntil != ""

	// 安装名迁移: 旧 wallpaper.exe → 统一 AIWallpaper.exe(重指计划任务/快捷方式/卸载项, 清理旧名)。
	// 运行中的旧 exe 无法删除: 改名为 .old, 由下次 tick 的 CleanupOldFiles 清理。
	// installed 守卫: 仅当运行于安装目录(bin\)时才执行迁移/重注册/快捷方式/卸载项等系统级副作用——
	// go test 等开发态进程的 exe 在 GOTMPDIR go-build 临时目录, 若不拦截会把计划任务/快捷方式
	// 劫持到临时路径(go 清理后即死路径, 实测事故: 任务每小时 0x80070002)。
	installed := env.ExePath != "" && filepath.Base(filepath.Dir(env.ExePath)) == "bin"
	if exe := env.ExePath; installed && !strings.EqualFold(filepath.Base(exe), install.ExeName) {
		binDir := filepath.Dir(exe)
		dst := install.BinPath(binDir)
		if err := install.CopyFile(exe, dst); err != nil {
			st.Log("tick: 安装名迁移复制失败: %v", err)
		} else if err := scheduler.Register(dst, scheduler.TickHours, cfg.PhaseMinutes); err != nil {
			st.Log("tick: 迁移后重注册计划任务失败: %v", err)
		} else {
			cfg.TaskTickHours = scheduler.TickHours
			_ = cfg.Save(env.Dir)
			_ = install.CreateShortcuts(dst)
			_ = install.WriteUninstallKey(dst, filepath.Dir(binDir), update.CurrentVersion)
			_ = os.Rename(exe, exe+".old")
			env.ExePath = dst // 后续自更新指向新路径
			st.Log("tick: 安装名已迁移 → %s", dst)
		}
	}

	// 计划任务节律修正(固定每小时; 补池与换图节流均由 tick 内部完成)。同 installed 守卫。
	if installed && cfg.TaskTickHours != taskTickHours {
		if err := scheduler.Register(env.ExePath, taskTickHours, cfg.PhaseMinutes); err == nil {
			cfg.TaskTickHours = taskTickHours
			_ = cfg.Save(env.Dir)
			st.Log("tick: 计划任务节律已修正为每 %d 小时", taskTickHours)
		}
	}

	// 无感自更新留痕: 运行版本与记录不一致 = 已完成一次静默换装(或首次就位),
	// 记录"最后更新时间"供设置页展示(全程无提示)。
	if cfg.LastVersion != update.CurrentVersion {
		cfg.LastUpdate = now.Format(time.RFC3339)
		cfg.LastVersion = update.CurrentVersion
		_ = cfg.Save(env.Dir)
		st.Log("tick: 记录最后更新时间 → %s (版本 %s)", cfg.LastUpdate, update.CurrentVersion)
	}

	profile, err := config.LoadProfile(env.Dir)
	if err != nil {
		st.Log("tick: 画像读取失败: %v", err)
		profile = config.NewProfile()
	}
	history, _ := st.LoadHistory()

	// ---------- 云端 / 直连(不可达也不终止: 本地备用池仍可换图) ----------
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
		if h, err := cc.Health(ctx); err != nil {
			st.Log("tick: 云端不可达(%v), 仅使用本地备用池", err)
		} else if !h.QuotaOK {
			st.Log("tick: 云端配额用尽, 仅使用本地备用池")
		} else {
			cloudOK = true
			cc.FlushQueue(ctx, env.Dir) // 尽力补传历史离线记录
			be = &cloud.Backend{C: cc}
		}
	}

	// ---------- 运营策略同步(失败沿用本地缓存/内置默认) ----------
	pol := policy.Load(env.Dir)
	if cloudOK {
		if p, err := cc.Policy(ctx); err != nil {
			st.Log("tick: 策略同步失败(%v), 沿用缓存", err)
		} else if p != nil {
			pol = p.Normalize()
			if err := policy.Save(env.Dir, pol); err != nil {
				st.Log("tick: 策略缓存写入失败: %v", err)
			}
		}
	}

	sw, sh := env.Screen()

	// ---------- 节律判定: 本轮是否进入换图窗口 ----------
	changeOK, skipReason := changeWindow(env, cfg, pol, now, paused)
	if !changeOK {
		out.Detail = "本轮不换图: " + skipReason
		st.Log("tick: 本轮不换图(%s), 仅后台维护", skipReason)
	}

	// ---------- 满意度回访(仅换图窗口内; 关闭回访开关则整体跳过) ----------
	forceShake := false
	if changeOK {
		forceShake = handleSatisfaction(env, cfg, st, now)
	}

	// 信号快照须在回访之后加载(回访可能写入新信号, 本轮一并上报)
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

	// 本地备用池优先(换风格轮除外): 已生成未使用, 断网可换
	if changeOK && !forceShake {
		if e, img, err := pool.Take(env.Dir); err == nil && e != nil && desktop.Inspect(img) == nil {
			imgData = img
			source = "pool"
			wfID = e.WorkflowID
			spec = prompt.Spec{Combo: e.Combo, Positive: e.Positive, Negative: e.Negative, Seed: e.Seed}
			if e.Width > 0 {
				genW, genH = e.Width, e.Height
			}
			st.Log("tick: 使用本地备用池(池余 %d)", pool.Count(env.Dir))
		}
	}

	if changeOK && imgData == nil && be != nil {
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
			out.Detail = fmt.Sprintf("出图失败: %v", err)
			st.Log("tick: 出图失败: %v, 保留当前壁纸", err)
			reportPrompt(env, cc, cfg, spec, genW, genH, wfID, source, duration, false)
		} else {
			imgData = img
		}
	}
	if changeOK && imgData == nil && out.Detail == "" {
		out.Detail = "暂无可用图源(云端不可达且备用池空)"
	}

	// ---------- 适配 + 换壁纸 ----------
	changed := false
	if imgData != nil {
		if final, err := desktop.FitToScreen(imgData, sw, sh); err != nil {
			out.Detail = "适配失败: " + err.Error()
			st.Log("tick: 适配失败: %v, 保留当前壁纸", err)
		} else if path, err := st.SaveWallpaper(final, now); err != nil {
			out.Detail = "留档失败: " + err.Error()
			st.Log("tick: 留档失败: %v", err)
		} else if err := env.SetWP(path); err != nil {
			out.Detail = "换壁纸失败: " + err.Error()
			st.Log("tick: 换壁纸失败: %v", err)
		} else {
			st.SetCurrent(path)
			changed = true
			out.Changed = true
			// 记录换图锚点(换图频率节流依据)
			cfg.LastChangeAt = now.Format(time.RFC3339)
			if err := cfg.Save(env.Dir); err != nil {
				st.Log("tick: 换图时间记录失败: %v", err)
			}
		}
	}

	// ---------- 补池 与 静默自更新: 并行执行(互不依赖, 谁也不拖住谁) ----------
	// 二者资源不相交(补池写 wallpapers/pool, 更新写 bin 并启换装助手); 助手等待旧进程
	// 退出的上限已放宽(见 update.ApplyUpdate), 因此补池拖长本轮也不会导致换装超时。
	var wg sync.WaitGroup
	if cloudOK {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replenishPool(ctx, env, st, cc, cfg, pol, profile, history, sw, sh)
		}()
	}
	if cc != nil && env.SelfUpdate && env.ExePath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			update.Check(ctx, cc, env.Dir, env.ExePath, nil, func(f string, a ...any) { st.Log("update: "+f, a...) })
		}()
	}
	wg.Wait()

	// ---------- 上报 + 历史 ----------
	if imgData != nil {
		reportPrompt(env, cc, cfg, spec, genW, genH, wfID, source, duration, changed)
		if changed {
			_ = st.AppendHistory(prompt.HistoryEntry{
				At: now, Combo: spec.Combo, Positive: spec.Positive,
				Negative: spec.Negative, Seed: spec.Seed, Source: source,
			})
		}
	}
	if cc != nil && len(pending) > 0 {
		if err := cc.ReportSignals(ctx, pending); err == nil {
			_ = signals.Clear(env.Dir)
		} else if err := cloud.Enqueue(env.Dir, "signals", pending); err == nil {
			_ = signals.Clear(env.Dir) // 已安全入补传队列(服务端按事件 ID 去重)
		}
	}

	_ = st.CleanupWallpapers(store.KeepWallpapers)
	st.Log("tick 完成: 换图=%v source=%s 池=%d/%d", changed, source, pool.Count(env.Dir), pol.PoolTarget)
	return nil
}

// ---------- 满意度回访 ----------

// handleSatisfaction 到期时弹回访小窗并应用选择; 返回是否"换个风格"轮。
// 回访开关关闭时整体跳过(包括首次排期)。
func handleSatisfaction(env Env, cfg *config.Config, st *store.Store, now time.Time) bool {
	if !cfg.AskEnabled {
		return false
	}
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

// taskTickHours 计划任务理想节律(小时); 与 scheduler.TickHours 统一, 换图频率由 tick 内部节流完成。
const taskTickHours = scheduler.TickHours

// changeWindow 判定本轮是否进入换图窗口, 并给出跳过原因(日志用)。
// 频率节流: 距上次成功换图不足 interval_hours 的 90% 时跳过(容忍计划任务抖动)。
func changeWindow(env Env, cfg *config.Config, pol policy.Policy, now time.Time, paused bool) (bool, string) {
	if env.Force {
		return true, "" // 手动触发(立即换一张): 用户明确指令, 绕过全部自动节律闸门
	}
	switch {
	case !cfg.AutoChange:
		return false, "换图开关已关闭"
	case paused:
		return false, "用户暂停中"
	case !pol.InActive(now):
		return false, "非活跃时段"
	case env.Quiet():
		return false, "锁屏/全屏"
	}
	if last, err := time.Parse(time.RFC3339, cfg.LastChangeAt); err == nil {
		gap := time.Duration(cfg.IntervalHours) * time.Hour
		if now.Sub(last) < gap*9/10 {
			return false, "换图频率未到"
		}
	}
	return true, ""
}

// maxPoolSubmitPerTick 单轮补池下单上限(默认目标 3 张; 策略调大时多轮自然补齐, 避免挤占云端)。
const maxPoolSubmitPerTick = 3

// replenishPool 补池: 本地备用池(含在途)不足目标时一次补齐缺额——单轮至多 maxPoolSubmitPerTick 单,
// 云端 idle 队列在普通出图空闲后逐张完成; 在途单逐单查询, 就绪的当轮全部提回池中。
// 静默期照常执行, 保证断网/出图拥挤时仍有成品可换; 储备充足则延缓申请避免拥堵。
func replenishPool(ctx context.Context, env Env, st *store.Store, cc *cloud.Client, cfg *config.Config, pol policy.Policy, profile *config.Profile, history []prompt.HistoryEntry, sw, sh int) {
	if n := pool.CleanVersion(env.Dir, cfg.ProfileVer); n > 0 {
		st.Log("tick: 清理失效备用池 %d 张", n)
	}
	dirty := false
	// 在途单清理: 画像版本过期 / 超过 24h 未完成
	pendings := pool.LoadPendings(env.Dir)
	kept := make([]pool.Pending, 0, len(pendings))
	for _, p := range pendings {
		if p.ProfileVersion != cfg.ProfileVer {
			dirty = true
			continue
		}
		if t, err := time.Parse(time.RFC3339, p.Since); err == nil && env.Now().Sub(t) > 24*time.Hour {
			st.Log("tick: 补池在途超时, 已放弃重试")
			dirty = true
			continue
		}
		kept = append(kept, p)
	}
	pendings = kept

	// 提货: 遍历在途单, 就绪的逐一提回池中(一次可提多张)
	rest := make([]pool.Pending, 0, len(pendings))
	for _, p := range pendings {
		info, err := cc.PregenFetch(ctx, cfg.ProfileVer, p.JobID)
		if err != nil || info == nil || !info.Ready {
			rest = append(rest, p)
			continue
		}
		img, err := cc.PregenImage(ctx, p.JobID)
		if err != nil || desktop.Inspect(img) != nil {
			st.Log("tick: 补池提回失败(job=%s), 下轮重试", p.JobID)
			rest = append(rest, p)
			continue
		}
		if err := pool.Add(env.Dir, pool.Entry{
			ProfileVersion: cfg.ProfileVer,
			Positive:       info.Positive,
			Negative:       info.Negative,
			Combo:          info.Combo,
			Seed:           info.Seed,
			WorkflowID:     info.WorkflowID,
			Width:          info.Width,
			Height:         info.Height,
			CreatedAt:      env.Now().Format(time.RFC3339),
		}, img); err != nil {
			st.Log("tick: 补池入池失败: %v", err)
			rest = append(rest, p)
			continue
		}
		dirty = true
		st.Log("tick: 补池成功(池 %d/%d)", pool.Count(env.Dir), pol.PoolTarget)
	}
	pendings = rest

	// 提服务端主动预生成: 云端空闲调度器代下的 ready 图(本地无在途单), 有则秒提入池,
	// 使本轮无需现场等待; 提走即删, 不会与自己在途单重复。
	for pool.Count(env.Dir) < pol.PoolTarget {
		info, err := cc.PregenFetch(ctx, cfg.ProfileVer, "")
		if err != nil || info == nil || !info.Ready || info.JobID == "" {
			break
		}
		if info.ProfileVersion != cfg.ProfileVer {
			break // 版本不符(改偏好前残留), 留给 CleanVersion 清理, 不提
		}
		img, err := cc.PregenImage(ctx, info.JobID)
		if err != nil || desktop.Inspect(img) != nil {
			st.Log("tick: 主动预生成提回失败(job=%s), 下轮重试", info.JobID)
			break
		}
		if err := pool.Add(env.Dir, pool.Entry{
			ProfileVersion: info.ProfileVersion,
			Positive:       info.Positive,
			Negative:       info.Negative,
			Combo:          info.Combo,
			Seed:           info.Seed,
			WorkflowID:     info.WorkflowID,
			Width:          info.Width,
			Height:         info.Height,
			CreatedAt:      env.Now().Format(time.RFC3339),
		}, img); err != nil {
			st.Log("tick: 主动预生成入池失败: %v", err)
			break
		}
		dirty = true
		st.Log("tick: 提主动预生成入池(池 %d/%d)", pool.Count(env.Dir), pol.PoolTarget)
	}

	// 下单: 不足目标时补齐缺额(单轮上限; 云端 idle 队列自动等出图机空闲)
	need := pol.PoolTarget - pool.Count(env.Dir) - len(pendings)
	if need > maxPoolSubmitPerTick {
		need = maxPoolSubmitPerTick
	}
	submitted := 0
	for i := 0; i < need; i++ {
		jobID, err := cc.PregenSubmit(ctx, cloud.PregenReq{
			ProfileVersion: cfg.ProfileVer,
			Profile:        profile.Weights,
			Disliked:       profile.Disliked,
			CustomKeywords: profile.CustomKeywords,
			RecentRounds:   recentRounds(history, 10),
			ScreenW:        sw,
			ScreenH:        sh,
			DisableContext: cfg.DisableContext,
		})
		if err != nil {
			st.Log("tick: 补池下单失败: %v", err)
			break
		}
		pendings = append(pendings, pool.Pending{
			JobID:          jobID,
			ProfileVersion: cfg.ProfileVer,
			Since:          env.Now().Format(time.RFC3339),
		})
		dirty = true
		submitted++
	}
	if submitted > 0 {
		st.Log("tick: 补池下单 %d 张(池 %d/%d, 在途 %d)", submitted, pool.Count(env.Dir), pol.PoolTarget, len(pendings))
	}
	if dirty {
		_ = pool.SavePendings(env.Dir, pendings)
	}
}
