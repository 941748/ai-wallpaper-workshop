package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"wallpaper/cloud/store"
	"wallpaper/internal/backend"
)

// ---------- 出图 ----------

func (a *API) handleGenerate(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	// device_id 仅用于配额作用域判定(安卓独立12/天), 不参与出图参数
	var raw struct {
		backend.GenParams
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&raw); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	p := raw.GenParams
	if !a.checkGenQuota(u, raw.DeviceID) {
		quotaFail(w)
		return
	}
	a.bumpGen(u, raw.DeviceID)

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
	if err := a.st.RecordSiteEvent("img_dl", u.UserID, siteVisitorIP(r)); err != nil {
		a.logf("下载图计数失败: %v", err)
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
	a.saveProfileSnapshot(u.UserID, req)
	jobID := a.submitPregenOrder(u, req)
	if jobID == "" {
		writeFail(w, http.StatusInternalServerError, "预生成排队失败")
		return
	}
	writeOK(w, map[string]any{"queued": true, "job_id": jobID})
}

// saveProfileSnapshot 持久化客户端随行画像快照(供空闲预生成调度器在客户端离线时仍能代下单)。
func (a *API) saveProfileSnapshot(userID string, req nextReq) {
	if req.ProfileVersion <= 0 && len(req.Profile) == 0 {
		return
	}
	oldSnap, _ := a.st.LoadProfileSnapshot(userID)
	// 方案A: 携带 device_id 的请求把屏幕写进 devices(设备维度); 安卓设备不回写共享快照的
	// screen 字段 —— 快照 screen 语义保留为"PC 主屏", 供预生成兜底, 避免双端比例互相污染。
	keepScreen := false
	if req.DeviceID != "" {
		if isAndroid, err := a.st.DeviceIsAndroid(req.DeviceID, userID); err == nil && isAndroid {
			keepScreen = true
		}
		if err := a.st.TouchDeviceScreen(req.DeviceID, userID, req.ScreenW, req.ScreenH); err != nil {
			a.logf("设备屏幕记录失败 user=%s device=%s: %v", userID, req.DeviceID, err)
		}
	}
	snap := store.ProfileSnapshot{
		UserID: userID, ProfileVersion: req.ProfileVersion,
		ScreenW: req.ScreenW, ScreenH: req.ScreenH,
	}
	if keepScreen {
		if oldSnap != nil {
			snap.ScreenW, snap.ScreenH = oldSnap.ScreenW, oldSnap.ScreenH
		} else {
			snap.ScreenW, snap.ScreenH = 0, 0
		}
	}
	if len(req.Profile) > 0 {
		if b, err := json.Marshal(req.Profile); err == nil {
			snap.Profile = string(b)
		}
	}
	if len(req.Disliked) > 0 {
		if b, err := json.Marshal(req.Disliked); err == nil {
			snap.Disliked = string(b)
		}
	}
	if len(req.CustomKeywords) > 0 {
		if b, err := json.Marshal(req.CustomKeywords); err == nil {
			snap.Custom = string(b)
		}
	}
	// 风格包必须随快照落库(nil=旧客户端/未设置→留空走权重反推; "[]"=显式清空)
	if req.Packs != nil {
		if b, err := json.Marshal(req.Packs); err == nil {
			snap.Packs = string(b)
		}
	}
	// 版本守卫(双端同步): 上传 pv 落后于服务端(安卓编辑经 POST /profile 已授更高 pv)时,
	// 画像字段保持服务端现值, 只让 screen/设备随请求更新 —— 防旧画像覆盖新编辑。
	if oldSnap != nil && req.ProfileVersion < oldSnap.ProfileVersion {
		snap.Profile = oldSnap.Profile
		snap.Disliked = oldSnap.Disliked
		snap.Custom = oldSnap.Custom
		snap.Packs = oldSnap.Packs
		snap.ProfileVersion = oldSnap.ProfileVersion
	}
	if err := a.st.SaveProfileSnapshot(snap); err != nil {
		a.logf("画像快照保存失败 user=%s: %v", userID, err)
	}
}

// submitPregenOrder 预生成下单公共路径(配额计数+LLM 规划+idle 排队+登记), 返回 job_id(空=失败)。
// 客户端主动补池与云端空闲主动预生成共用, 保证两者语义与配额记账一致。
func (a *API) submitPregenOrder(u *store.User, req nextReq) string {
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
		a.logf("预生成排队失败 user=%s: %v", u.UserID, err)
		return ""
	}
	_ = a.st.InsertPregen(store.Pregen{
		UserID: u.UserID, ProfileVersion: req.ProfileVersion, JobID: jobID,
		Positive: dec.Positive, Negative: dec.Negative, Combo: comboJSON,
		Seed: dec.Seed, WorkflowID: dec.WorkflowID, Width: dec.Width, Height: dec.Height,
	})
	a.logf("预生成下单 user=%s job=%s pv=%d", u.UserID, jobID, req.ProfileVersion)
	return jobID
}

func (a *API) handlePregenFetch(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	pv, _ := strconv.Atoi(r.URL.Query().Get("profile_version"))
	// job_id 可选: 新版客户端按单精确查询(补池多单); 缺省时返回"当前版本最早就绪"一条(兼容老客户端)。
	var p *store.Pregen
	var err error
	if jobID := r.URL.Query().Get("job_id"); jobID != "" {
		p, err = a.st.PeekPregenByJob(u.UserID, jobID)
	} else {
		p, err = a.st.PeekPregenReady(u.UserID, pv)
	}
	if err != nil || p == nil || !p.Ready {
		writeOK(w, map[string]any{"ready": false, "profile_version": pv})
		return
	}
	var combo map[string]string
	if p.Combo != "" {
		_ = json.Unmarshal([]byte(p.Combo), &combo)
	}
	writeOK(w, map[string]any{
		"ready": true, "profile_version": p.ProfileVersion, "job_id": p.JobID,
		"positive": p.Positive, "negative": p.Negative, "combo": combo,
		"seed": p.Seed, "workflow_id": p.WorkflowID, "width": p.Width, "height": p.Height,
	})
}

func (a *API) handlePregenImage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	// job_id 可选: 新版客户端按单提货(提走即删); 缺省时提"当前版本最早就绪"一条(兼容老客户端)。
	var p *store.Pregen
	var err error
	if jobID := r.URL.Query().Get("job_id"); jobID != "" {
		p, err = a.st.PopPregenByJob(u.UserID, jobID)
	} else {
		p, err = a.st.PopPregenReady(u.UserID)
	}
	if err != nil || p == nil || !p.Ready || p.ImagePath == "" {
		writeFail(w, http.StatusNotFound, "暂无预生成成品")
		return
	}
	if err := a.st.RecordSiteEvent("img_dl", u.UserID, siteVisitorIP(r)); err != nil {
		a.logf("下载图计数失败: %v", err)
	}
	serveFile(w, p.ImagePath)
}
