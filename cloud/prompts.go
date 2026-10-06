package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

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

// decidePrompt 生成本轮决策: LLM 优先生成(其输出经校验), 失败回退本地引擎。
func (a *API) decidePrompt(ctx context.Context, userID string, req nextReq) nextResp {
	wf := a.reg.Resolve("")
	if a.llm.Enabled() {
		system := a.systemPrompt()
		user := a.userPrompt(userID, req)
		start := time.Now()
		content, err := a.llm.ChatJSON(ctx, system, user)
		if err == nil {
			if resp, ok := a.parseLLMOut(content, req); ok {
				return resp
			}
			a.logf("LLM 输出校验失败, 回退本地引擎 (user=%s, out=%s)", userID, snippet(content, 300))
		} else {
			a.logf("LLM 调用失败(耗时 %dms): %v (user=%s)", time.Since(start).Milliseconds(), err, userID)
		}
	}
	spec := localCompose(req)
	width, height := BucketSize(req.ScreenW, req.ScreenH, wf)
	return nextResp{
		Positive: spec.Positive, Negative: spec.Negative,
		Width: width, Height: height, WorkflowID: wf.ID,
		Combo: spec.Combo, Seed: spec.Seed,
		Reason: "本地引擎(画像加权采样)", Source: "local",
	}
}

// snippet 截断字符串用于日志(rune 安全)。
func snippet(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// parseLLMOut 宽容解析并校验 LLM 输出(白名单/尺寸/字段完整性)。
// 兼容不稳定的输出类型: 数字可为字符串/浮点; combo 可为对象或字符串化对象。
func (a *API) parseLLMOut(content string, req nextReq) (nextResp, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(extractJSON(content)), &raw); err != nil {
		a.logf("LLM 输出 JSON 解析失败: %v (out=%s)", err, snippet(content, 500))
		return nextResp{}, false
	}
	positive := strOf(raw["positive"])
	if strings.TrimSpace(positive) == "" {
		a.logf("LLM 输出 positive 为空 (out=%s)", snippet(content, 500))
		return nextResp{}, false
	}
	if len(positive) > 1200 {
		positive = positive[:1200]
	}
	negative := strOf(raw["negative"])
	if len(negative) > 800 {
		negative = negative[:800]
	}
	wf := a.reg.Resolve(strOf(raw["workflow_id"]))
	w0, h0 := intOf(raw["width"]), intOf(raw["height"])
	width, height := ClampSize(wf, w0, h0)
	if w0 == 0 || h0 == 0 {
		width, height = BucketSize(req.ScreenW, req.ScreenH, wf)
	}
	combo := comboOf(raw["combo"])
	if len(combo) == 0 {
		combo = topCombo(req.Profile)
	}
	seed := int64(intOf(raw["seed"]))
	if seed <= 0 {
		seed = time.Now().UnixNano() & 0x7fffffff
	}
	return nextResp{
		Positive: positive, Negative: negative,
		Width: width, Height: height, WorkflowID: wf.ID,
		Combo: combo, Seed: seed, Reason: strOf(raw["reason"]), Source: "llm",
	}, true
}

// strOf 宽容取字符串(非字符串返回空)。
func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// intOf 宽容取整数(兼容浮点与数字字符串)。
func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return int(f)
	}
	return 0
}

// comboOf 宽容解析五维组合: 对象直接取; 字符串形态(可能被字符串化的 JSON)尽力解析;
// 非法键值由 sanitizeCombo 白名单剔除。
func comboOf(v any) map[string]string {
	collect := func(m map[string]any) map[string]string {
		out := map[string]string{}
		for k, vv := range m {
			if s, ok := vv.(string); ok {
				out[k] = s
			}
		}
		return sanitizeCombo(out)
	}
	switch c := v.(type) {
	case map[string]any:
		return collect(c)
	case string:
		var nested map[string]any
		if err := json.Unmarshal([]byte(c), &nested); err == nil {
			return collect(nested)
		}
	}
	return nil
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

// sanitizeCombo 仅保留合法维值。
func sanitizeCombo(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, d := range taxonomy.Dimensions {
		if v, ok := in[d.ID]; ok && taxonomy.ValueOf(d.ID, v) != nil {
			out[d.ID] = v
		}
	}
	return out
}

// topCombo 画像最高权重组合(全维)。
func topCombo(weights map[string]float64) map[string]string {
	out := map[string]string{}
	for _, d := range taxonomy.Dimensions {
		best, bestW := "", math.Inf(-1)
		for _, v := range d.Values {
			w := weights[config.Key(d.ID, v.ID)]
			if w == 0 {
				w = 0.5
			}
			if w > bestW {
				best, bestW = v.ID, w
			}
		}
		out[d.ID] = best
	}
	return out
}

// localCompose 本地引擎兜底(与客户端一致的加权采样/探索/去重规则)。
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

func (a *API) systemPrompt() string {
	// 词典摘要
	var dims []string
	for _, d := range taxonomy.Dimensions {
		var vals []string
		for _, v := range d.Values {
			vals = append(vals, v.ID+"("+v.NameCN+")")
		}
		dims = append(dims, d.ID+" 可选: "+strings.Join(vals, ", "))
	}
	var wfs []string
	for _, w := range a.reg.List() {
		desc := w.ID + "(" + w.Name + ")"
		if len(w.Loras) > 0 {
			names := make([]string, 0, len(w.Loras))
			for _, l := range w.Loras {
				names = append(names, l.Name)
			}
			desc += " LoRA:" + strings.Join(names, "+")
		}
		wfs = append(wfs, desc)
	}
	return "你是壁纸出图规划器。根据用户的偏好画像、最近壁纸记录与漂移事件, 规划下一张壁纸。" +
		"输出必须是 JSON 对象, 字段: positive(英文自然语言描述, 60-120 词, 面向 Z-Image Turbo), " +
		"negative(英文负向提示词, 当前工作流不使用, 可留空), width/height(整数, 8 的倍数, 512~1920, 默认 1920/1080), " +
		"workflow_id(必须从工作流白名单选择), " +
		"combo(JSON 对象, 键为 style/subject/palette/mood/composition, 值为词典值 ID), seed(整数), reason(一句中文理由)。\n" +
		"五维词典: " + strings.Join(dims, "; ") + "。\n" +
		"工作流白名单: " + strings.Join(wfs, "; ") + "。\n" +
		"规则: 结合画像高权值选择组合; 避开 disliked 中的元素; 若存在 disliked 事件, 必须避开其记录的组合要素; " +
		"若存在 liked 事件, 在后续画面中多呼应其组合要素; 若提供 自定义偏好关键词, 在合适的画面中自然融入(不必每张出现, 不得生硬堆砌); " +
		"若存在强烈拒斥信号(style_reject)或 force_shake, 必须给出与最近记录显著不同的风格; " +
		"若存在 style_keep 或多次满意, 保持当前方向并做微变化; " +
		"禁止文字/水印/低质元素; 数字字段用 JSON 数字类型, combo 必须是 JSON 对象; 不得输出 JSON 以外的任何内容。"
}

func (a *API) userPrompt(userID string, req nextReq) string {
	type kv struct {
		name string
		w    float64
	}
	var tops []kv
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			w := req.Profile[config.Key(d.ID, v.ID)]
			if w >= 0.6 {
				tops = append(tops, kv{taxonomy.NameCN(d.ID, v.ID), w})
			}
		}
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].w > tops[j].w })
	var topList []string
	for _, e := range tops {
		topList = append(topList, fmt.Sprintf("%s=%.2f", e.name, e.w))
	}
	if len(topList) == 0 {
		topList = append(topList, "(中立, 无显著偏好)")
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
				var combo map[string]string
				if rec.Combo != "" {
					_ = json.Unmarshal([]byte(rec.Combo), &combo)
				}
				recent = append(recent, comboDesc(combo))
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
		"偏好高权值":    topList,
		"disliked":   req.Disliked,
		"最近壁纸记录":   recent,
		"漂移事件(近一周)": sigs,
		"屏幕分辨率":    []int{req.ScreenW, req.ScreenH},
		"force_shake": req.ForceShake,
	}
	if len(req.CustomKeywords) > 0 {
		body["自定义偏好关键词"] = req.CustomKeywords
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
