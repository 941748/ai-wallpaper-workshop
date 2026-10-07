package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wallpaper/cloud/llm"
	"wallpaper/cloud/store"
	"wallpaper/internal/backend"
	"wallpaper/internal/probe"
)

// API 云服务 HTTP 处理集合。
type API struct {
	st         *store.Store
	llm        *llm.Client
	reg        *Registry
	q          *queue
	dataDir    string
	siteDir    string
	dlDir      string
	adminToken string
	version    string
	logf       func(string, ...any)

	genHour, genDay int
	llmHour, llmDay int
}

// Routes 注册全部路由(Go 1.22 方法路由)。
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/users/register", a.handleRegister)
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
	mux.HandleFunc("POST /api/v1/prompts/next", a.handleNext)
	mux.HandleFunc("GET /api/v1/prompts/next", a.handleNext)
	mux.HandleFunc("POST /api/v1/prompts", a.handlePromptReport)
	mux.HandleFunc("POST /api/v1/signals", a.handleSignals)
	mux.HandleFunc("POST /api/v1/generate", a.handleGenerate)
	mux.HandleFunc("GET /api/v1/generate/{id}", a.handleJob)
	mux.HandleFunc("GET /api/v1/generate/{id}/image", a.handleJobImage)
	mux.HandleFunc("POST /api/v1/pregen", a.handlePregenSubmit)
	mux.HandleFunc("GET /api/v1/pregen", a.handlePregenFetch)
	mux.HandleFunc("GET /api/v1/pregen/image", a.handlePregenImage)
	mux.HandleFunc("GET /api/v1/client/latest", a.handleClientLatest)
	mux.HandleFunc("POST /api/v1/devices/link", a.handleLink)
	mux.HandleFunc("GET /api/v1/profile/drift", a.handleDrift)
	mux.HandleFunc("GET /api/v1/probes/sample", a.handleProbeSample)
	mux.HandleFunc("GET /api/v1/probes/image", a.handleProbeImage)
	mux.HandleFunc("GET /d/{code}", a.handleLinkPage)
	mux.HandleFunc("GET /admin", a.handleAdmin)
	// 客户端自更新分发
	mux.Handle("GET /releases/", http.StripPrefix("/releases/",
		http.FileServer(http.Dir(filepath.Join(a.dataDir, "releases")))))
	// 官网(推广与客户端下载)
	mux.HandleFunc("GET /", a.handleSite)
	mux.HandleFunc("GET /download/AIWallpaper.exe", a.handleDownload)
	return mux
}

// ---------- 官网 ----------

// handleSite 官网静态页(index.html 与 assets/); 非站点路径返回 404。
func (a *API) handleSite(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") {
		writeFail(w, http.StatusNotFound, "接口不存在")
		return
	}
	if p == "/" {
		p = "/index.html"
	}
	site, err := filepath.Abs(a.siteDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fp, err := filepath.Abs(filepath.Join(site, filepath.Clean(p)))
	if err != nil || !strings.HasPrefix(fp, site+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(p, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	http.ServeFile(w, r, fp)
}

// handleDownload 客户端安装包下载(支持 Range 断点续传)。
func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	fp := filepath.Join(a.dlDir, "AIWallpaper.exe")
	if _, err := os.Stat(fp); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="AIWallpaper-Setup.exe"`)
	http.ServeFile(w, r, fp)
}

// ---------- 基础工具 ----------

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, _ := json.Marshal(data)
	_, _ = w.Write([]byte(`{"success":true,"data":` + string(body) + `}`))
}

func writeFail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	b, _ := json.Marshal(msg)
	_, _ = w.Write([]byte(`{"success":false,"error":` + string(b) + `}`))
}

func quotaFail(w http.ResponseWriter) { writeFail(w, http.StatusTooManyRequests, "quota_exceeded") }

// authUser 校验 Bearer 令牌。
func (a *API) authUser(r *http.Request) *store.User {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return nil
	}
	u, err := a.st.UserByToken(token)
	if err != nil || u == nil || u.Disabled {
		return nil
	}
	return u
}

// requireUser 未认证时直接回 401。
func (a *API) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u := a.authUser(r)
	if u == nil {
		writeFail(w, http.StatusUnauthorized, "未授权的令牌")
		return nil, false
	}
	return u, true
}

func (a *API) checkQuota(u *store.User, kind string) bool {
	llmH, genH, err := a.st.UsageHour(u.UserID)
	if err != nil {
		return true
	}
	llmD, genD, err := a.st.UsageToday(u.UserID)
	if err != nil {
		return true
	}
	if kind == "llm" {
		return llmH < a.llmHour && llmD < a.llmDay
	}
	return genH < a.genHour && genD < a.genDay
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- 注册 / 健康 ----------

func (a *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device string `json:"device"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	userID := "u_" + randomHex(8)
	token := randomHex(24)
	if err := a.st.CreateUser(userID, token); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Device != "" {
		_ = a.st.AddDevice(body.Device, userID)
	}
	a.logf("注册新用户 %s (device=%s)", userID, body.Device)
	writeOK(w, map[string]string{"user_id": userID, "token": token})
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	quotaOK := true
	if u := a.authUser(r); u != nil {
		quotaOK = a.checkQuota(u, "gen") && a.checkQuota(u, "llm")
	}
	writeOK(w, map[string]any{"ok": true, "quota_ok": quotaOK, "server_version": a.version})
}

// ---------- 提示词决策 ----------

func (a *API) handleNext(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var req nextReq
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeFail(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
	}
	if !a.checkQuota(u, "llm") {
		quotaFail(w)
		return
	}
	_ = a.st.BumpUsage(u.UserID, "llm")

	// 持久化随行漂移事件(幂等)
	if len(req.PendingSignals) > 0 {
		a.persistSignals(u.UserID, req.PendingSignals)
		_ = a.st.UpsertDrift(u.UserID, a.driftSummary(u.UserID))
	}

	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	resp := a.decidePrompt(ctx, u.UserID, req)
	a.logf("提示词决策 user=%s source=%s wf=%s %dx%d", u.UserID, resp.Source, resp.WorkflowID, resp.Width, resp.Height)
	writeOK(w, resp)
}

func (a *API) handlePromptReport(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var rec struct {
		At         time.Time         `json:"at"`
		Positive   string            `json:"positive"`
		Negative   string            `json:"negative"`
		Combo      map[string]string `json:"combo"`
		Seed       int64             `json:"seed"`
		Width      int               `json:"width"`
		Height     int               `json:"height"`
		WorkflowID string            `json:"workflow_id"`
		Source     string            `json:"source"`
		DurationMs int64             `json:"duration_ms"`
		Success    bool              `json:"success"`
		ProfileVer int               `json:"profile_version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&rec); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	comboJSON := ""
	if len(rec.Combo) > 0 {
		if b, err := json.Marshal(rec.Combo); err == nil {
			comboJSON = string(b)
		}
	}
	err := a.st.InsertPrompt(u.UserID, store.PromptRecord{
		At: rec.At, Positive: rec.Positive, Negative: rec.Negative, Combo: comboJSON,
		Seed: rec.Seed, Width: rec.Width, Height: rec.Height, WorkflowID: rec.WorkflowID,
		Source: rec.Source, DurationMs: rec.DurationMs, Success: rec.Success, ProfileVer: rec.ProfileVer,
	})
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func (a *API) handleSignals(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Events []signalItem `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	a.persistSignals(u.UserID, body.Events)
	_ = a.st.UpsertDrift(u.UserID, a.driftSummary(u.UserID))
	writeOK(w, map[string]bool{"ok": true})
}

func (a *API) persistSignals(userID string, evs []signalItem) {
	var out []store.Signal
	for _, e := range evs {
		extra := ""
		if len(e.Extra) > 0 {
			if b, err := json.Marshal(e.Extra); err == nil {
				extra = string(b)
			}
		}
		out = append(out, store.Signal{ID: e.ID, At: e.At, Type: e.Type, Detail: e.Detail, Extra: extra})
	}
	if err := a.st.InsertSignals(userID, out); err != nil {
		a.logf("信号入库失败: %v", err)
	}
}

// ---------- 出图 ----------

func (a *API) handleGenerate(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var p backend.GenParams
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if !a.checkQuota(u, "gen") {
		quotaFail(w)
		return
	}
	_ = a.st.BumpUsage(u.UserID, "gen")

	wf := a.reg.Resolve(p.WorkflowID)
	width, height := ClampSize(wf, p.Width, p.Height)
	priority := "normal"
	if p.Priority == "idle" {
		priority = "idle"
	}
	jobID := randomHex(12)
	err := a.st.CreateJob(store.Job{
		JobID: jobID, UserID: u.UserID, Kind: "gen", Priority: priority,
		Positive: p.Positive, Negative: p.Negative,
		Width: width, Height: height, Seed: p.Seed, WorkflowID: wf.ID,
	})
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.logf("出图任务 user=%s job=%s %dx%d wf=%s", u.UserID, jobID, width, height, wf.ID)
	writeOK(w, map[string]string{"job_id": jobID})
}

func (a *API) handleJob(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	job, err := a.st.Job(id)
	if err != nil || job == nil {
		writeFail(w, http.StatusNotFound, "任务不存在")
		return
	}
	if job.UserID != u.UserID {
		writeFail(w, http.StatusForbidden, "无权访问该任务")
		return
	}
	writeOK(w, map[string]string{"status": job.Status, "error": job.Error})
}

func (a *API) handleJobImage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	job, err := a.st.Job(r.PathValue("id"))
	if err != nil || job == nil || job.UserID != u.UserID {
		writeFail(w, http.StatusNotFound, "任务不存在")
		return
	}
	if job.Status != "done" || job.ImagePath == "" {
		writeFail(w, http.StatusConflict, "成品尚未就绪")
		return
	}
	serveFile(w, job.ImagePath)
}

// ---------- 预生成(空闲队列) ----------

func (a *API) handlePregenSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var req nextReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if !a.checkQuota(u, "llm") {
		quotaFail(w)
		return
	}
	if !a.checkQuota(u, "gen") {
		quotaFail(w)
		return
	}
	_ = a.st.BumpUsage(u.UserID, "llm")
	_ = a.st.BumpUsage(u.UserID, "gen")

	// 预生成是"下单后异步完成"语义: 规划(LLM)与排队不依赖请求连接生命周期。
	// 此前绑 r.Context() 时, 客户端先行离开会过早取消 LLM, 导致预生成永远降级本地引擎。
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	dec := a.decidePrompt(ctx, u.UserID, req)

	comboJSON := ""
	if len(dec.Combo) > 0 {
		if b, err := json.Marshal(dec.Combo); err == nil {
			comboJSON = string(b)
		}
	}
	jobID := "pg-" + randomHex(10)
	if err := a.st.CreateJob(store.Job{
		JobID: jobID, UserID: u.UserID, Kind: "pregen", Priority: "idle",
		Positive: dec.Positive, Negative: dec.Negative, Combo: comboJSON,
		Width: dec.Width, Height: dec.Height, Seed: dec.Seed, WorkflowID: dec.WorkflowID,
	}); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.st.UpsertPregen(store.Pregen{
		UserID: u.UserID, ProfileVersion: req.ProfileVersion, JobID: jobID,
		Positive: dec.Positive, Negative: dec.Negative, Combo: comboJSON,
		Seed: dec.Seed, WorkflowID: dec.WorkflowID, Width: dec.Width, Height: dec.Height,
	})
	a.logf("预生成下单 user=%s job=%s pv=%d", u.UserID, jobID, req.ProfileVersion)
	writeOK(w, map[string]any{"queued": true, "job_id": jobID})
}

func (a *API) handlePregenFetch(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	pv, _ := strconv.Atoi(r.URL.Query().Get("profile_version"))
	p, err := a.st.GetPregen(u.UserID)
	if err != nil || p == nil || !p.Ready || p.ProfileVersion != pv {
		writeOK(w, map[string]any{"ready": false, "profile_version": pv})
		return
	}
	var combo map[string]string
	if p.Combo != "" {
		_ = json.Unmarshal([]byte(p.Combo), &combo)
	}
	writeOK(w, map[string]any{
		"ready": true, "profile_version": p.ProfileVersion,
		"positive": p.Positive, "negative": p.Negative, "combo": combo,
		"seed": p.Seed, "workflow_id": p.WorkflowID, "width": p.Width, "height": p.Height,
	})
}

func (a *API) handlePregenImage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	p, err := a.st.GetPregen(u.UserID)
	if err != nil || p == nil || !p.Ready || p.ImagePath == "" {
		writeFail(w, http.StatusNotFound, "暂无预生成成品")
		return
	}
	serveFile(w, p.ImagePath)
}

// ---------- 客户端版本分发 ----------

func (a *API) handleClientLatest(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("version")
	manifestPath := filepath.Join(a.dataDir, "releases", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		// 未发布过版本: 返回同版本(无更新)
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	var m struct {
		Version string `json:"version"`
		File    string `json:"file"`
		SHA256  string `json:"sha256"`
		Size    int64  `json:"size"`
		Notes   string `json:"notes"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version == "" || m.File == "" {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	if compareVer(m.Version, current) <= 0 {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	if os.Getenv("AW_FORCE_HTTPS") == "1" {
		scheme = "https"
	}
	writeOK(w, map[string]any{
		"version": m.Version,
		"url":     fmt.Sprintf("%s://%s/releases/%s", scheme, r.Host, m.File),
		"sha256":  m.SHA256, "size": m.Size, "notes": m.Notes,
	})
}

func compareVer(a, b string) int {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}

// ---------- 设备扫码配对 ----------

func (a *API) handleLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Code   string `json:"code"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	switch body.Action {
	case "create":
		u, ok := a.requireUser(w, r)
		if !ok {
			return
		}
		// 生成 6 位随机配对码(冲突则重试)
		var code string
		for i := 0; i < 8; i++ {
			n := 0
			for _, b := range []byte(randomHex(4)) {
				n = (n*16 + int(b)%16) % 1000000
			}
			if n < 100000 {
				n += 100000
			}
			code = strconv.Itoa(n)
			exp := time.Now().Add(5 * time.Minute)
			if err := a.st.CreateTicket(code, u.UserID, exp); err == nil {
				writeOK(w, map[string]any{
					"code":       code,
					"url":        fmt.Sprintf("https://%s/d/%s", hostOf(r), code),
					"expires_at": exp.Format(time.RFC3339),
				})
				return
			}
		}
		writeFail(w, http.StatusInternalServerError, "配对码生成失败, 请重试")
	case "redeem":
		userID, ok, err := a.st.RedeemTicket(strings.TrimSpace(body.Code), time.Now())
		if err != nil {
			writeFail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeFail(w, http.StatusNotFound, "配对码无效或已过期")
			return
		}
		u, err := a.st.UserByID(userID)
		if err != nil || u == nil {
			writeFail(w, http.StatusNotFound, "用户不存在")
			return
		}
		if body.Device != "" {
			_ = a.st.AddDevice(body.Device, u.UserID)
		}
		a.logf("设备配对成功 user=%s device=%s", u.UserID, body.Device)
		writeOK(w, map[string]string{"user_id": u.UserID, "token": u.Token})
	default:
		writeFail(w, http.StatusBadRequest, "未知 action")
	}
}

func hostOf(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

// handleLinkPage 手机扫码打开的中转页(极简 H5, 非客户端界面)。
func (a *API) handleLinkPage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s 设备配对</title>
<style>body{font-family:system-ui;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#eee}
.code{font-size:48px;letter-spacing:8px;font-weight:700;margin:16px 0;color:#7dd3fc}
.hint{color:#999;font-size:14px;max-width:80%%;text-align:center}</style></head><body>
<div>在新设备的"同步"页输入配对码:</div><div class="code">%s</div>
<div class="hint">配对码 5 分钟内有效, 仅可使用一次。此页面用于向新设备转发配对码, 可直接在手机上查看。</div>
</body></html>`, appNameCN, code)
}

// ---------- 漂移摘要 ----------

func (a *API) handleDrift(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	summary := a.driftSummary(u.UserID)
	_ = a.st.UpsertDrift(u.UserID, summary)
	writeOK(w, map[string]string{"summary": summary})
}

// ---------- 公共探针池 ----------

var probeIDRe = regexp.MustCompile(`^p\d{2}$`)

func (a *API) handleProbeSample(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 || count > probe.FirstCount {
		count = probe.FirstCount
	}
	combos := probe.BalancedCombos(probe.FirstCount)
	items := make([]map[string]any, 0, count)
	for i, cb := range combos {
		if i >= count {
			break
		}
		items = append(items, map[string]any{
			"id":     cb.ID,
			"values": cb.Values,
			"seed":   cb.Seed,
			"url":    "/api/v1/probes/image?id=" + cb.ID,
		})
	}
	writeOK(w, map[string]any{"items": items})
}

func (a *API) handleProbeImage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !probeIDRe.MatchString(id) {
		writeFail(w, http.StatusBadRequest, "非法探针 ID")
		return
	}
	path := filepath.Join(a.dataDir, "probes", id+".png")
	if _, err := os.Stat(path); err != nil {
		writeFail(w, http.StatusNotFound, "探针图尚未就绪")
		return
	}
	serveFile(w, path)
}

func serveFile(w http.ResponseWriter, path string) {
	f, err := os.Open(path)
	if err != nil {
		writeFail(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer f.Close()
	if strings.HasSuffix(path, ".png") {
		w.Header().Set("Content-Type", "image/png")
	}
	_, _ = io.Copy(w, f)
}
