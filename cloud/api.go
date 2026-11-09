package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"wallpaper/cloud/llm"
	"wallpaper/cloud/store"
)

// 本文件仅保留 API 结构、路由注册与跨 handler 复用的基础工具;
// 各领域 handler 按主题拆分至 handlers_site / handlers_user / handlers_prompt /
// handlers_render / handlers_update / scheduler / admin。

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
	androidGenDay   int // 安卓端独立出图日配额(与 PC 同账号但分开计数)
	agentSeen       atomic.Int64   // 代理最近一次请求( Unix 秒; 0=从未连接), 供 health/看板
	agentVer        atomic.Value   // string: 代理自报版本(claim 携带)
}

// agentVersion 代理自报版本(空=未上报)。
func (a *API) agentVersion() string {
	if v, ok := a.agentVer.Load().(string); ok {
		return v
	}
	return ""
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
	mux.HandleFunc("POST /api/v1/images/delete", a.handleImageDelete)
	mux.HandleFunc("GET /api/v1/history", a.handleHistory)
	mux.HandleFunc("POST /api/v1/history/fav", a.handleHistoryFav)
	mux.HandleFunc("GET /api/v1/history/{id}/image", a.handleHistoryImage)
	mux.HandleFunc("POST /api/v1/pregen", a.handlePregenSubmit)
	mux.HandleFunc("GET /api/v1/pregen", a.handlePregenFetch)
	mux.HandleFunc("GET /api/v1/pregen/image", a.handlePregenImage)
	mux.HandleFunc("GET /api/v1/policy", a.handlePolicy)
	mux.HandleFunc("GET /api/v1/client/latest", a.handleClientLatest)
	mux.HandleFunc("POST /api/v1/devices/link", a.handleLink)
	mux.HandleFunc("GET /api/v1/devices", a.handleDevices)
	mux.HandleFunc("GET /api/v1/profile/drift", a.handleDrift)
	mux.HandleFunc("GET /api/v1/profile", a.handleProfile)
	mux.HandleFunc("POST /api/v1/profile", a.handleProfileSave)
	mux.HandleFunc("GET /api/v1/android/latest", a.handleAndroidLatest)
	mux.HandleFunc("GET /d/{code}", a.handleLinkPage)
	mux.HandleFunc("GET /admin", a.handleAdmin)
	mux.HandleFunc("GET /admin/migrate-oss", a.handleMigrateOSS)
	mux.HandleFunc("GET /admin/echarts.min.js", a.handleEcharts)
	// 客户端自更新分发
	mux.Handle("GET /releases/", http.StripPrefix("/releases/",
		http.FileServer(http.Dir(filepath.Join(a.dataDir, "releases")))))
	// 官网(推广与客户端下载)
	mux.HandleFunc("GET /", a.handleSite)
	mux.HandleFunc("GET /download/AIWallpaper.exe", a.handleDownload)
	mux.HandleFunc("GET /go/dl", a.handleGoDL)
	mux.HandleFunc("GET /go/apk", a.handleGoAPK)
	mux.HandleFunc("GET /go/agent", a.handleGoAgent)
	mux.HandleFunc("POST /api/v1/agent/claim", a.handleAgentClaim)
	mux.HandleFunc("GET /api/v1/agent/latest", a.handleAgentLatest)
	mux.HandleFunc("POST /api/v1/agent/rendered", a.handleAgentRendered)
	return mux
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

// requireUser 未认证时直接回 401; 同时刷新活跃时间(供空闲预生成调度器判定 24h 活跃)。
func (a *API) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u := a.authUser(r)
	if u == nil {
		writeFail(w, http.StatusUnauthorized, "未授权的令牌")
		return nil, false
	}
	_ = a.st.TouchUser(u.UserID)
	return u, true
}

func (a *API) checkQuota(u *store.User, kind string) bool {
	return a.checkQuotaScope(u.UserID, kind)
}

// checkQuotaScope 按作用域查配额(scope=用户ID 或 uid@android)。
func (a *API) checkQuotaScope(scope, kind string) bool {
	llmH, genH, err := a.st.UsageHour(scope)
	if err != nil {
		return true
	}
	llmD, genD, err := a.st.UsageToday(scope)
	if err != nil {
		return true
	}
	if kind == "llm" {
		return llmH < a.llmHour && llmD < a.llmDay
	}
	return genH < a.genHour && genD < a.genDay
}

// genScope 安卓设备的出图计数独立作用域(uid+@android), PC/未知设备保持同账号共享。
// 移动端与 PC 配额互不影响(移动端默认 12 张/天, 见 AW_QUOTA_ANDROID_DAY)。
func (a *API) genScope(u *store.User, deviceID string) string {
	if deviceID == "" {
		return u.UserID
	}
	if ok, err := a.st.DeviceIsAndroid(deviceID, u.UserID); err == nil && ok {
		return u.UserID + "@android"
	}
	return u.UserID
}

// checkGenQuota 按设备作用域检查出图配额(小时上限共用 genHour, 日上限安卓用 androidGenDay)。
func (a *API) checkGenQuota(u *store.User, deviceID string) bool {
	scope := a.genScope(u, deviceID)
	_, genH, err := a.st.UsageHour(scope)
	if err != nil {
		return true
	}
	_, genD, err := a.st.UsageToday(scope)
	if err != nil {
		return true
	}
	day := a.genDay
	if scope != u.UserID {
		day = a.androidGenDay
	}
	return genH < a.genHour && genD < day
}

// bumpGen 按设备作用域记一次出图用量。
func (a *API) bumpGen(u *store.User, deviceID string) {
	_ = a.st.BumpUsage(a.genScope(u, deviceID), "gen")
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// serveFile 以只读方式回传成品文件(.png/.jpg 自动置图片 MIME)。
func serveFile(w http.ResponseWriter, path string) {
	f, err := os.Open(path)
	if err != nil {
		writeFail(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer f.Close()
	switch {
	case strings.HasSuffix(path, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		w.Header().Set("Content-Type", "image/jpeg")
	}
	_, _ = io.Copy(w, f)
}
