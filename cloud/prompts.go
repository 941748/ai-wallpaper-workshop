package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"wallpaper/internal/almanac"
	"wallpaper/internal/config"
	"wallpaper/internal/prompt"
	"wallpaper/internal/taxonomy"
)

// nextReq 客户端请求本轮提示词。
type nextReq struct {
	ProfileVersion int                `json:"profile_version"`
	Profile        map[string]float64 `json:"profile"`
	Disliked       []string           `json:"disliked"`
	CustomKeywords []string           `json:"custom_keywords"`
	RecentRounds   []roundItem        `json:"recent_rounds"`
	PendingSignals []signalItem       `json:"pending_signals"`
	ScreenW        int                `json:"screen_w"`
	ScreenH        int                `json:"screen_h"`
	ForceShake     bool               `json:"force_shake"`
	DisableContext bool               `json:"disable_context"`
}

type roundItem struct {
	At     time.Time         `json:"at"`
	Combo  map[string]string `json:"combo"`
	Source string            `json:"source"`
}

type signalItem struct {
	ID     string         `json:"id"`
	At     time.Time      `json:"at"`
	Type   string         `json:"type"`
	Detail string         `json:"detail"`
	Extra  map[string]any `json:"extra"`
}

// nextResp 云端决策结果(提示词 + 尺寸 + 工作流)。
type nextResp struct {
	Positive   string            `json:"positive"`
	Negative   string            `json:"negative"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	WorkflowID string            `json:"workflow_id"`
	Combo      map[string]string `json:"combo,omitempty"`
	Seed       int64             `json:"seed"`
	Reason     string            `json:"reason"`
	Source     string            `json:"source"` // llm | local
}

// decidePrompt 生成本轮决策: 五维组合由本地引擎按画像确定(风格/内容严格跟随偏好),
// LLM 仅负责把组合润色为自然语言画面描述(可融入语境, 不得改动组合); 失败回退本地拼装。
func (a *API) decidePrompt(ctx context.Context, userID string, req nextReq) nextResp {
	wf := a.reg.Resolve("")
	spec := localCompose(req)
	width, height := BucketSize(req.ScreenW, req.ScreenH, wf)
	resp := nextResp{
		Positive: spec.Positive, Negative: spec.Negative,
		Width: width, Height: height, WorkflowID: wf.ID,
		Combo: spec.Combo, Seed: spec.Seed,
		Reason: "本地引擎(画像加权采样)", Source: "local",
	}
	if a.llm.Enabled() {
		system := a.systemPrompt()
		user := a.userPrompt(userID, req, spec.Combo)
		start := time.Now()
		content, err := a.llm.ChatJSON(ctx, system, user)
		if err == nil {
			if pos, reason, ok := parsePolish(content); ok {
				resp.Positive = pos
				if reason != "" {
					resp.Reason = reason
				}
				resp.Source = "llm"
				a.logf("LLM 文案成功(耗时 %dms) (user=%s)", time.Since(start).Milliseconds(), userID)
				return resp
			}
			a.logf("LLM 文案校验失败, 回退本地拼装 (user=%s, out=%s)", userID, snippet(content, 300))
		} else {
			a.logf("LLM 调用失败(耗时 %dms, ctxErr=%v): %v (user=%s)",
				time.Since(start).Milliseconds(), ctx.Err(), err, userID)
		}
	}
	return resp
}

// snippet 截断字符串用于日志(rune 安全)。
func snippet(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// parsePolish 解析并校验 LLM 文案输出: 仅取 positive(画面描述)与 reason;
// 兼容 markdown 包裹与多余字段; positive 缺失视为无效。
func parsePolish(content string) (string, string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(extractJSON(content)), &raw); err != nil {
		return "", "", false
	}
	pos := strings.TrimSpace(strOf(raw["positive"]))
	if pos == "" {
		return "", "", false
	}
	if len(pos) > 1200 {
		pos = pos[:1200]
	}
	return pos, strOf(raw["reason"]), true
}

// strOf 宽容取字符串(非字符串返回空)。
func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// extractJSON 容错提取 JSON(LLM 偶尔包 markdown 代码块)。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	return s
}

// localCompose 本地引擎(与客户端一致的加权采样/去重规则; 组合由画像确定, 也是兜底文案的拼装器)。
func localCompose(req nextReq) prompt.Spec {
	p := config.NewProfile()
	p.Weights = map[string]float64{}
	for k, v := range req.Profile {
		p.Weights[k] = v
	}
	p.Disliked = req.Disliked
	var hist []prompt.HistoryEntry
	for _, r := range req.RecentRounds {
		hist = append(hist, prompt.HistoryEntry{At: r.At, Combo: r.Combo, Source: r.Source})
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return prompt.Compose(p, rng, prompt.Options{ForceShake: req.ForceShake, History: hist, CustomKeywords: req.CustomKeywords})
}

// signalDesc 将漂移事件转为供 LLM 理解的描述; 评价类事件附带画面组合。
func signalDesc(typ string, extra map[string]any) string {
	if typ == "liked" || typ == "disliked" {
		if combo := anyCombo(extra); len(combo) > 0 {
			if typ == "liked" {
				return "liked(用户点了喜欢: " + comboDesc(combo) + ")"
			}
			return "disliked(用户点了不喜欢: " + comboDesc(combo) + ")"
		}
	}
	return typ
}

// contextHints 构建"近期节日节气"语境条目(关闭开关或无事件时返回 nil)。
func contextHints(now time.Time, disable bool) []string {
	if disable {
		return nil
	}
	evs := almanac.Within(now, 3)
	if len(evs) == 0 {
		return nil
	}
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		line := e.Date.Format("01-02") + " " + e.Name + ": " + e.Scene
		if e.Sensitive {
			line += "(庄重含蓄, 提醒非庆祝)"
		}
		out = append(out, line)
	}
	return out
}

// anyCombo 从事件 Extra 中提取五维组合(JSON 反序列化后为 map[string]any)。
func anyCombo(extra map[string]any) map[string]string {
	if extra == nil {
		return nil
	}
	m, ok := extra["combo"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// ---------- LLM 提示词 ----------

// systemPrompt 生成者角色: 组合已定, 只写画面, 不得改动组合。
func (a *API) systemPrompt() string {
	return "你是一个智慧高清壁纸生成器, 你也是心理学、美术绘画、摄影、新闻宣传、艺术设计专家。" +
		"你根据用户提供的 5 个维度信息(风格/题材/配色/氛围/构图), 结合当前的季节、节气、节日、天气等, " +
		"输出用于 AI 绘画软件的专业的提示词, 以生成符合用户偏好的高清壁纸。\n" +
		"你的唯一任务: 为给定组合撰写 60-120 词的英文自然语言画面描述(供 Z-Image Turbo 出图, 面向 16:9 桌面壁纸)。\n" +
		"硬性要求:\n" +
		"1. 画面必须忠实呈现组合中的每一个维度; 风格维度严禁偏离或混入其他风格" +
		"(例: 组合为写实(realism)时, 不得出现 watercolor/oil painting/anime/illustration 等词);\n" +
		"2. 可自然融入给定语境(最近壁纸记录、漂移事件、节日节气氛围、自定义偏好关键词), 但不得改变组合要素;\n" +
		"3. 禁止文字/水印/横幅/边框元素; 只输出正向画面描述。\n" +
		"输出必须是 JSON 对象: {\"positive\": \"...\", \"reason\": \"一句中文理由\"}, 不得输出 JSON 以外的任何内容。"
}

// userPrompt 构造润色请求: 已定组合(中文 + 各维英文参考片段)与语境(最近记录/漂移/节日/自定义词)。
func (a *API) userPrompt(userID string, req nextReq, combo map[string]string) string {
	var refs []string
	for _, d := range taxonomy.Dimensions {
		v := taxonomy.ValueOf(d.ID, combo[d.ID])
		if v == nil {
			continue
		}
		refs = append(refs, d.ID+"("+v.NameCN+"): "+v.Prompt)
	}

	var recent []string
	for _, r := range req.RecentRounds {
		if len(recent) >= 10 {
			break
		}
		recent = append(recent, comboDesc(r.Combo))
	}
	if len(recent) == 0 {
		// 服务端记录兜底
		if recs, err := a.st.RecentPrompts(userID, 10); err == nil {
			for _, rec := range recs {
				if len(recent) >= 10 {
					break
				}
				var c map[string]string
				if rec.Combo != "" {
					_ = json.Unmarshal([]byte(rec.Combo), &c)
				}
				recent = append(recent, comboDesc(c))
			}
		}
	}

	var sigs []string
	for _, s := range req.PendingSignals {
		sigs = append(sigs, signalDesc(s.Type, s.Extra))
	}
	if len(sigs) == 0 {
		if evs, err := a.st.RecentSignals(userID, time.Now().AddDate(0, 0, -7), 20); err == nil {
			for _, e := range evs {
				sigs = append(sigs, e.Type)
			}
		}
	}
	if len(sigs) == 0 {
		sigs = append(sigs, "(近一周无漂移事件)")
	}

	body := map[string]any{
		"组合":        comboDesc(combo),
		"组合参考片段":    refs,
		"最近壁纸记录":    recent,
		"漂移事件(近一周)": sigs,
	}
	if len(req.CustomKeywords) > 0 {
		body["自定义偏好关键词"] = req.CustomKeywords
	}
	if hints := contextHints(time.Now(), req.DisableContext); len(hints) > 0 {
		body["近期节日节气"] = hints
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func comboDesc(combo map[string]string) string {
	if len(combo) == 0 {
		return "(无记录)"
	}
	var parts []string
	for _, d := range taxonomy.Dimensions {
		if v, ok := combo[d.ID]; ok && v != "" {
			parts = append(parts, taxonomy.NameCN(d.ID, v))
		}
	}
	if len(parts) == 0 {
		return "(无记录)"
	}
	return strings.Join(parts, "+")
}

// ---------- 漂移摘要 ----------

// driftSummary 生成用户近一周漂移摘要(规则版; LLM 已用于每轮决策)。
func (a *API) driftSummary(userID string) string {
	since := time.Now().AddDate(0, 0, -7)
	rej, _ := a.st.CountSignals(userID, "style_reject", since)
	keep, _ := a.st.CountSignals(userID, "style_keep", since)
	conf, _ := a.st.CountSignals(userID, "reconfigure", since)
	rea, _ := a.st.CountSignals(userID, "reapply", since)
	var parts []string
	if rej > 0 {
		parts = append(parts, fmt.Sprintf("主动换风格 %d 次", rej))
	}
	if keep > 0 {
		parts = append(parts, fmt.Sprintf("确认满意 %d 次", keep))
	}
	if conf > 0 {
		parts = append(parts, fmt.Sprintf("重新配置 %d 次", conf))
	}
	if rea > 0 {
		parts = append(parts, fmt.Sprintf("重应用历史壁纸 %d 次", rea))
	}
	if len(parts) == 0 {
		return "近一周无明显偏好漂移"
	}
	return "近一周: " + strings.Join(parts, ", ")
}
