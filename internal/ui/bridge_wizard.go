package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/signals"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// ---------- 连接测试(设置页"测试连接"共用) ----------

// ConnReq 连接步骤请求(云端唯一模式, 保留结构体便于后续扩展)。
type ConnReq struct{}

// ConnResult 连接测试结果。
type ConnResult struct {
	OK      bool   `json:"ok"`
	Render  bool   `json:"render"`
	Message string `json:"message"`
}

// TestConnection 测试连接(设置页): 云端可达性+配额+出图链路。
func (b *Bridge) TestConnection(req ConnReq) ConnResult {
	b.ensureIdentity() // 首装离线时的注册自愈
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

// snapshot 读当前配置副本(不持锁时的安全读; 仅只读字段)。
func (b *Bridge) snapshot() *config.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := *b.cfg
	return &c
}

// ---------- 问卷/偏好(设置页"偏好"共用) ----------

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

// GetSurvey 返回词典与当前画像状态(设置页偏好用)。
//
// 词典数据源: **服务端字典优先(与安卓偏好页同源)** —— 词典/风格包变更只需部署服务端,
// 双端客户端不用发版; 3 秒拉不到(断网/慢网)回退本地内嵌词典, 偏好页永不空白。
// 选中包按画像原名原样往返(不再按本地词典过滤), 未知包名不丢。
func (b *Bridge) GetSurvey() SurveyData {
	b.mu.Lock()
	cc := b.cc
	disableCtx := b.cfg.DisableContext
	b.mu.Unlock()

	profile, _ := config.LoadProfile(b.dir)
	if profile == nil {
		profile = config.NewProfile()
	}
	answers := answersFromProfile(profile)
	selNames := profilePackNames(profile)
	dims, packInfos := surveyDict(cc)
	out := SurveyData{
		Dims:          dims,
		Packs:         packInfos,
		Answers:       map[string]int{},
		SelectedPacks: selNames,
		Likes:         surveyLikesIn(answers),
		Dislikes:      surveyDislikesIn(answers),
		Summary:       packSummaryNames(selNames) + focusSummary(answers),
		Recap:         surveyRecapNames(answers, selNames),
		Keywords:      append([]string{}, profile.CustomKeywords...),
		DisableCtx:    disableCtx,
	}
	if !profile.UpdatedAt.IsZero() {
		out.UpdatedAtDays = int(time.Since(profile.UpdatedAt).Hours() / 24)
	}
	for k, a := range answers {
		out.Answers[k] = int(a)
	}
	return out
}

// surveyDict 词典数据源: 服务端字典优先, 失败回退本地词典。
func surveyDict(cc *cloud.Client) ([]SurveyDim, []PackInfo) {
	if cc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if rp, err := cc.GetProfile(ctx); err == nil && rp.Dict != nil && len(rp.Dict.Dimensions) > 0 {
			return dictToSurvey(rp.Dict)
		}
	}
	// 本地兜底(与旧版行为一致): 词典来自本二进制内嵌 taxonomy
	var dims []SurveyDim
	for _, d := range taxonomy.Dimensions {
		sd := SurveyDim{ID: d.ID, NameCN: d.NameCN}
		for _, v := range d.Values {
			sd.Values = append(sd.Values, SurveyVal{ID: v.ID, NameCN: v.NameCN})
		}
		dims = append(dims, sd)
	}
	var packs []PackInfo
	for _, p := range taxonomy.PresetPacks {
		info := PackInfo{Name: p.Name, Vals: map[string][]string{}}
		for _, d := range taxonomy.Dimensions {
			if vs := p.Values(d.ID); len(vs) > 0 {
				info.Vals[d.ID] = vs
			}
		}
		packs = append(packs, info)
	}
	return dims, packs
}

// dictToSurvey 服务端词典 → 问卷数据(covers "dim/val" 拆成 Vals 分维列表)。
func dictToSurvey(d *cloud.Dict) ([]SurveyDim, []PackInfo) {
	var dims []SurveyDim
	for _, dd := range d.Dimensions {
		sd := SurveyDim{ID: dd.ID, NameCN: dd.Name}
		for _, v := range dd.Values {
			sd.Values = append(sd.Values, SurveyVal{ID: v.ID, NameCN: v.Name})
		}
		dims = append(dims, sd)
	}
	var packs []PackInfo
	for _, p := range d.Packs {
		info := PackInfo{Name: p.Name, Vals: map[string][]string{}}
		for _, c := range p.Covers {
			if dim, val, ok := strings.Cut(c, "/"); ok && dim != "" && val != "" {
				info.Vals[dim] = append(info.Vals[dim], val)
			}
		}
		packs = append(packs, info)
	}
	return dims, packs
}

// SaveSurvey 保存问卷(校验 + 落盘画像 + 版本递增 + 漂移信号)。
// Reset=true 表示"重置后保存": 在干净底版上应用本次选择(旧五维权重/点踩全丢,
// 自定义关键词保留) —— 与安卓暂存重置同语义, 单次原子写、版本只 +1。
func (b *Bridge) SaveSurvey(req struct {
	Answers       map[string]int `json:"answers"`
	SelectedPacks []string       `json:"selectedPacks"`
	Reason        string         `json:"reason"` // 信号明细(来源区分: 设置页调整/重置后重新选择)
	Reset         bool           `json:"reset"`
}) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(req.SelectedPacks) == 0 && countLikes(req.Answers) == 0 {
		return fmt.Errorf("请至少选择 1 个风格包或勾选 1 项喜欢")
	}
	var profile *config.Profile
	if req.Reset {
		old, _ := config.LoadProfile(b.dir)
		profile = config.NewProfile()
		profile.CustomKeywords = old.CustomKeywords
	} else {
		profile, _ = config.LoadProfile(b.dir)
	}
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
