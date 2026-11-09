package ui

import (
	"context"
	"fmt"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/install"
	"wallpaper/internal/prompt"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
	"wallpaper/internal/update"
)

// ---------- 向导: 连接步骤 ----------

// ConnReq 连接步骤请求(云端唯一模式, 保留结构体便于后续扩展)。
type ConnReq struct{}

// ConnResult 连接测试结果。
type ConnResult struct {
	OK      bool   `json:"ok"`
	Render  bool   `json:"render"`
	Message string `json:"message"`
}

// TestConnection 测试连接(向导/设置页共用): 云端可达性+配额+出图链路。
func (b *Bridge) TestConnection(req ConnReq) ConnResult {
	cfg := b.snapshot()
	_ = req
	renderOK, err := testConnection(cfg)
	switch {
	case err != nil:
		b.st.Log("连接测试失败: %v", err)
		return ConnResult{OK: false, Message: "连接失败: " + friendlyErrText(err) + ", 请稍后重试"}
	case !renderOK:
		return ConnResult{OK: true, Message: "连接成功 ✓ 当前出图排队较多, 将自动无感补图"}
	default:
		return ConnResult{OK: true, Render: true, Message: "连接成功 ✓ 云端服务可用, 出图服务就绪, 配额正常"}
	}
}

// SaveConn 保存连接配置并匿名注册(向导"下一步")。
func (b *Bridge) SaveConn(req ConnReq) ConnResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = req
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := ensureRegistered(ctx, b.cfg); err != nil {
		b.st.Log("SaveConn 注册失败: %v", err)
		return ConnResult{Message: "暂时无法连接服务(" + friendlyErrText(err) + "), 请稍后重试。"}
	}
	if err := b.cfg.Save(b.dir); err != nil {
		return ConnResult{Message: "配置保存失败: " + friendlyErrText(err)}
	}
	b.be, b.cc = makeBackend(b.cfg)
	pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Second)
	b.renderOK = b.be.Reachable(pctx)
	pcancel()
	return ConnResult{OK: true, Render: b.renderOK}
}

// snapshot 读当前配置副本(不持锁时的安全读; 仅只读字段)。
func (b *Bridge) snapshot() *config.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := *b.cfg
	return &c
}

// ---------- 向导: 问卷步骤 ----------

// SurveyVal 词条(前端筹码)。
type SurveyVal struct {
	ID     string `json:"id"`
	NameCN string `json:"nameCn"`
}

// SurveyDim 维度(前端分组)。
type SurveyDim struct {
	ID     string      `json:"id"`
	NameCN string      `json:"nameCn"`
	Values []SurveyVal `json:"values"`
}

// PackInfo 风格包(前端包行)。
type PackInfo struct {
	Name string              `json:"name"`
	Vals map[string][]string `json:"vals"` // dimID -> 值ID列表(覆盖高亮用)
}

// SurveyData 问卷全量数据(词典 + 当前状态)。
type SurveyData struct {
	Dims          []SurveyDim      `json:"dims"`
	Packs         []PackInfo       `json:"packs"`
	Answers       map[string]int   `json:"answers"` // "dim/val" -> 0中立 1喜欢 2讨厌
	SelectedPacks []string         `json:"selectedPacks"`
	Likes         int              `json:"likes"`
	Dislikes      int              `json:"dislikes"`
	Summary       string           `json:"summary"`
	Recap         string           `json:"recap"`
	Keywords      []string         `json:"keywords"`
	DisableCtx    bool             `json:"disableCtx"`
	UpdatedAtDays int              `json:"updatedDays"`
}

// GetSurvey 返回词典与当前画像状态(向导与设置页共用)。
func (b *Bridge) GetSurvey() SurveyData {
	b.mu.Lock()
	defer b.mu.Unlock()
	profile, _ := config.LoadProfile(b.dir)
	answers := answersFromProfile(profile)
	packs := initSelectedPacks(profile)
	out := SurveyData{
		Answers:       map[string]int{},
		SelectedPacks: selectedPackNames(packs),
		Likes:         surveyLikesIn(answers),
		Dislikes:      surveyDislikesIn(answers),
		Summary:       packSummary(packs) + focusSummary(answers),
		Recap:         surveyRecapText(answers, packs),
		Keywords:      append([]string{}, profile.CustomKeywords...),
		DisableCtx:    b.cfg.DisableContext,
	}
	if !profile.UpdatedAt.IsZero() {
		out.UpdatedAtDays = int(time.Since(profile.UpdatedAt).Hours() / 24)
	}
	for k, a := range answers {
		out.Answers[k] = int(a)
	}
	for _, d := range taxonomy.Dimensions {
		sd := SurveyDim{ID: d.ID, NameCN: d.NameCN}
		for _, v := range d.Values {
			sd.Values = append(sd.Values, SurveyVal{ID: v.ID, NameCN: v.NameCN})
		}
		out.Dims = append(out.Dims, sd)
	}
	for _, p := range taxonomy.PresetPacks {
		info := PackInfo{Name: p.Name, Vals: map[string][]string{}}
		for _, d := range taxonomy.Dimensions {
			if vs := p.Values(d.ID); len(vs) > 0 {
				info.Vals[d.ID] = vs
			}
		}
		out.Packs = append(out.Packs, info)
	}
	return out
}

// SaveSurvey 保存问卷(校验 + 落盘画像 + 版本递增 + 漂移信号)。
func (b *Bridge) SaveSurvey(req struct {
	Answers       map[string]int `json:"answers"`
	SelectedPacks []string       `json:"selectedPacks"`
	Reason        string         `json:"reason"` // 信号明细(向导/设置页区分)
}) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(req.SelectedPacks) == 0 && countLikes(req.Answers) == 0 {
		return fmt.Errorf("请至少选择 1 个风格包或勾选 1 项喜欢")
	}
	profile, _ := config.LoadProfile(b.dir)
	att := map[string]survey.Attitude{}
	for k, v := range req.Answers {
		att[k] = survey.Attitude(v)
	}
	survey.Apply(profile, att)
	profile.Packs = req.SelectedPacks // 显式落盘(非 nil, 即使清空)
	if err := profile.Save(b.dir); err != nil {
		return fmt.Errorf("偏好保存失败: %s", friendlyErrText(err))
	}
	b.cfg.ProfileVer++
	_ = b.cfg.Save(b.dir)
	detail := req.Reason
	if detail == "" {
		detail = fmt.Sprintf("调整偏好: 风格包 %d 个 · 喜欢 %d 项 / 讨厌 %d 项",
			len(req.SelectedPacks), countLikes(req.Answers), countDislikes(req.Answers))
	}
	_ = signals.Append(b.dir, signals.Event{Type: signals.TypeWeightAdjust, Detail: detail})
	b.be, b.cc = makeBackend(b.cfg) // profile 变更影响出图链路参数
	return nil
}

// ResetPrefs 清空风格包与五维偏好(自定义关键词保留)。
func (b *Bridge) ResetPrefs() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	profile, _ := config.LoadProfile(b.dir)
	profile.Disliked = nil
	profile.Packs = []string{}
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			profile.Set(d.ID, v.ID, survey.NeutralWeight)
		}
	}
	if err := profile.Save(b.dir); err != nil {
		return fmt.Errorf("重置失败: %s", friendlyErrText(err))
	}
	b.cfg.ProfileVer++
	_ = b.cfg.Save(b.dir)
	_ = signals.Append(b.dir, signals.Event{Type: signals.TypeReconfigure, Detail: "重置全部偏好"})
	return nil
}

// SaveKeyword 添加/删除自定义关键词(op=add|del, index 对 del 有效)。
func (b *Bridge) SaveKeyword(op string, word string, index int) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	profile, _ := config.LoadProfile(b.dir)
	switch op {
	case "add":
		w := trimSpace(word)
		if w == "" {
			return profile.CustomKeywords, nil
		}
		profile.CustomKeywords = config.NormalizeKeywords(append(profile.CustomKeywords, w))
	case "del":
		if index < 0 || index >= len(profile.CustomKeywords) {
			return profile.CustomKeywords, fmt.Errorf("请先选中要删除的关键词")
		}
		profile.CustomKeywords = append(profile.CustomKeywords[:index], profile.CustomKeywords[index+1:]...)
	}
	if err := profile.Save(b.dir); err != nil {
		return nil, fmt.Errorf("关键词保存失败: %s", friendlyErrText(err))
	}
	b.cfg.ProfileVer++
	_ = b.cfg.Save(b.dir)
	_ = signals.Append(b.dir, signals.Event{Type: signals.TypeKeywordsAdjust, Detail: "自定义关键词: " + op + " " + word})
	return append([]string{}, profile.CustomKeywords...), nil
}

// SetContextMode 跟随节日/节气氛围开关。
func (b *Bridge) SetContextMode(enable bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg.DisableContext = !enable
	return b.cfg.Save(b.dir)
}

// ---------- 向导: 首张壁纸 ----------

// FirstResult 首张壁纸生成结果。
type FirstResult struct {
	OK      bool   `json:"ok"`
	Busy    bool   `json:"busy"` // 链路不可用/超时 → 前端显示"繁忙稍后补图"
	Image   string `json:"image,omitempty"`
	Message string `json:"message,omitempty"`
}

// GenFirstWallpaper 生成并应用首张壁纸(force=换一张, 排除最近主风格)。
// 优先提服务端 ready 预生成(秒出), 无则现场出图(90s 上限)。阻塞至完成。
func (b *Bridge) GenFirstWallpaper(force bool) FirstResult {
	b.mu.Lock()
	cfg := *b.cfg
	profile, _ := config.LoadProfile(b.dir)
	cc, be := b.cc, b.be
	st := b.st
	b.mu.Unlock()

	// 1) ready 预生成秒提
	if cc != nil && !force {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		info, err := cc.PregenFetch(ctx, cfg.ProfileVer, "")
		if err == nil && info != nil && info.Ready && info.JobID != "" {
			img, ierr := cc.PregenImage(ctx, info.JobID)
			cancel()
			if ierr == nil && len(img) > 0 {
				return b.finishFirst(img, prompt.Spec{
					Positive: info.Positive, Negative: info.Negative,
					Combo: info.Combo, Seed: info.Seed,
				}, "pregen")
			}
			return FirstResult{Busy: true, Message: msgRenderBusy}
		}
		cancel()
	}
	// 2) 现场出图(90s)
	ctx, cancel := context.WithTimeout(context.Background(), firstWallpaperTimeout)
	defer cancel()
	history, _ := st.LoadHistory()
	data, spec, src, err := generateWallpaper(ctx, &cfg, cc, be, profile, history, force)
	if err != nil {
		st.Log("向导: 生成失败: %v", err)
		return FirstResult{Busy: true, Message: msgRenderBusy}
	}
	return b.finishFirst(data, spec, src)
}

// finishFirst 留档 + 设为壁纸 + 记历史 + 返回预览 base64。
func (b *Bridge) finishFirst(data []byte, spec prompt.Spec, src string) FirstResult {
	b.mu.Lock()
	st, cfg := b.st, b.cfg
	b.mu.Unlock()
	path, err := st.SaveWallpaper(data, time.Now())
	if err == nil {
		err = desktop.SetWallpaper(path)
		st.SetCurrent(path)
	}
	if err != nil {
		st.Log("向导: 壁纸应用失败: %v", err)
		return FirstResult{Busy: true, Message: msgRenderBusy}
	}
	_ = st.AppendHistory(prompt.HistoryEntry{
		At: time.Now(), Combo: spec.Combo, Positive: spec.Positive,
		Negative: spec.Negative, Seed: spec.Seed, Source: src,
	})
	_ = cfg // 首张完成不改配置
	b64, _ := scaleToB64(data, 480, 270)
	return FirstResult{OK: true, Image: b64, Message: "已应用为桌面壁纸 ✓"}
}

// ---------- 向导: 完成 ----------

// FinishResult 完成步骤(静默安装)结果。
type FinishResult struct {
	RegOK   bool   `json:"regOk"`
	Message string `json:"message"`
	Recap   string `json:"recap"`
}

// FinishWizard 记喜欢信号(首张被接受时) + 静默安装(计划任务/快捷方式/卸载项)。
func (b *Bridge) FinishWizard(accepted bool) FinishResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if accepted {
		_ = signals.Append(b.dir, signals.Event{Type: signals.TypeStyleKeep, Detail: "初始化首张壁纸: 喜欢"})
	}
	exe, err := exePath()
	if err != nil {
		b.st.Log("向导: 定位程序路径失败: %v", err)
		return FinishResult{RegOK: false, Message: "自动换图将由系统后台稍后完成注册; 不影响你正常使用"}
	}
	b.cfg.PhaseMinutes = phaseFromUser(b.cfg.UserID)
	dst, err := install.Setup(b.st.BinDir(), exe, update.CurrentVersion, b.cfg.PhaseMinutes)
	if err != nil {
		b.st.Log("向导: 安装/注册失败: %v", err)
		_ = b.cfg.Save(b.dir)
		return FinishResult{RegOK: false, Message: "自动换图将由系统后台稍后完成注册; 不影响你正常使用"}
	}
	b.cfg.TaskTickHours = scheduler.TickHours
	b.cfg.LastVersion = update.CurrentVersion
	if b.cfg.LastUpdate == "" {
		b.cfg.LastUpdate = time.Now().Format(time.RFC3339)
	}
	_ = b.cfg.Save(b.dir)
	// 标记初始化完成
	b.cfg.Initialized = true
	_ = b.cfg.Save(b.dir)
	profile, _ := config.LoadProfile(b.dir)
	answers := answersFromProfile(profile)
	packs := initSelectedPacks(profile)
	b.st.Log("向导: 已安装并注册计划任务 → %s", dst)
	return FinishResult{
		RegOK:   true,
		Message: fmt.Sprintf("已开启每小时自动换图 ✓ 桌面与开始菜单已添加\"%s\"快捷方式", config.AppNameCN),
		Recap:   surveyRecapText(answers, packs),
	}
}

func countLikes(a map[string]int) int {
	n := 0
	for _, v := range a {
		if v == 1 {
			n++
		}
	}
	return n
}

func countDislikes(a map[string]int) int {
	n := 0
	for _, v := range a {
		if v == 2 {
			n++
		}
	}
	return n
}
