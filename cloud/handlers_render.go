package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
	// P5: jobs 落库存物理屏尺寸(全链路物理口径); 渲染限宽由出图机按自身 max_side 自算,
	// 渲染后放大回物理再上传——键/归档/历史过滤自此全部=设备规格。
	priority := "normal"
	if p.Priority == "idle" {
		priority = "idle"
	}
	jobID := randomHex(8) // P6: 8字节=16hex(存量24hex共存, 消费方均为不透明字符串)
	err := a.st.CreateJob(store.Job{
		JobID: jobID, UserID: u.UserID, Kind: "gen", Priority: priority,
		Positive: p.Positive, Negative: p.Negative,
		Width: p.Width, Height: p.Height, Seed: p.Seed, WorkflowID: wf.ID,
	})
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.logf("出图任务 user=%s job=%s 物理%dx%d wf=%s", u.UserID, jobID, p.Width, p.Height, wf.ID)
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
	if job.Status != "done" || (job.ImagePath == "" && job.OssKey == "") {
		writeFail(w, http.StatusConflict, "成品尚未就绪")
		return
	}
	if err := a.st.RecordSiteEvent("img_dl", u.UserID, siteVisitorIP(r)); err != nil {
		a.logf("下载图计数失败: %v", err)
	}
	// 双模: 图片在 OSS → 302 签名 URL 直下(云服不碰字节); 否则回退本地文件
	if job.OssKey != "" {
		if u, err := presignOSSGET(job.OssKey, 15*time.Minute); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, u, http.StatusFound)
			return
		} else {
			a.logf("OSS 预签名失败, 回退本地文件: %v", err)
		}
	}
	serveFile(w, job.ImagePath)
}

// handleImageDelete 用户删除图片: 清 OSS 对象+本地文件+归档/任务引用(幂等, 仅本人)。
// 归档为长期真相(jobs 7 天即 Purge), 两行并存时都校验属主;
// OSS 清理失败返回500且不改任何行(客户端据此降级只删本地, 云端留待重试)。
func (a *API) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.JobID == "" {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	job, _ := a.st.Job(req.JobID)     // 可能已被 7 天清理
	arc, _ := a.st.Archive(req.JobID) // 归档长期存活
	if job == nil && arc == nil {
		writeFail(w, http.StatusNotFound, "任务不存在")
		return
	}
	if (job != nil && job.UserID != u.UserID) || (arc != nil && arc.UserID != u.UserID) {
		writeFail(w, http.StatusNotFound, "任务不存在")
		return
	}
	ossKey, imagePath := "", ""
	if arc != nil {
		ossKey, imagePath = arc.OssKey, arc.ImagePath
	}
	if job != nil {
		if ossKey == "" {
			ossKey = job.OssKey
		}
		if imagePath == "" {
			imagePath = job.ImagePath
		}
	}
	if ossKey == "" && imagePath == "" {
		if job != nil {
			_ = a.st.ClearJobImage(job.JobID)
		}
		writeOK(w, map[string]bool{"deleted": true})
		return
	}
	if ossKey != "" {
		if err := ossDeleteObject(ossKey); err != nil {
			a.logf("删除 OSS 对象失败 job=%s: %v", req.JobID, err)
			writeFail(w, http.StatusInternalServerError, "云端清理失败, 请稍后再试")
			return
		}
	}
	if arc != nil {
		_ = a.st.DeleteArchive(arc.JobID)
	}
	if job != nil {
		_ = a.st.ClearJobImage(job.JobID)
		if job.Kind == "pregen" {
			_ = a.st.ClearPregenImage(job.JobID)
		}
	}
	if imagePath != "" {
		if err := os.Remove(imagePath); err != nil {
			a.logf("删除本地文件失败 job=%s: %v", req.JobID, err)
		}
	}
	a.logf("删除图片 user=%s job=%s", u.UserID, req.JobID)
	writeOK(w, map[string]bool{"deleted": true})
}

// handleHistory 用户图片历史(归档表): jobs 7 天即 Purge, 历史独立存活。
// image_url: OSS 行=15分钟预签名直链; 本地行=历史取图端点(带 Bearer)。
// ?fav=1 仅看收藏; 收藏恒置顶。items 附提示词/seed 供展示与同款复现。
func (a *API) handleHistory(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	onlyFav := r.URL.Query().Get("fav") == "1"
	// ?w=&h= 按出图规格过滤(按设备取回: 客户端上报本机屏幕, 只看同规格组)
	fw, _ := strconv.Atoi(r.URL.Query().Get("w"))
	fh, _ := strconv.Atoi(r.URL.Query().Get("h"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	rows, err := a.st.ListArchive(u.UserID, onlyFav, fw, fh, offset, limit)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		// positive(提示词)为内部留存数据; image_url 双端前端均不读(预览走
		// /history/{id}/image 端点) —— 都不下发, 每行省一次签名与几十字节
		items = append(items, map[string]any{
			"job_id": row.JobID, "created_at": row.CreatedAt,
			"width": row.Width, "height": row.Height, "kind": row.Kind,
			"fav": row.Fav == 1, "seed": row.Seed,
		})
	}
	writeOK(w, map[string]any{"items": items})
}

// handleHistoryFav 收藏/取消收藏(收藏驱动 history 置顶与 ?fav=1 筛选)。
func (a *API) handleHistoryFav(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		JobID string `json:"job_id"`
		Fav   bool   `json:"fav"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.JobID == "" {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	row, err := a.st.Archive(req.JobID)
	if err != nil || row == nil || row.UserID != u.UserID {
		writeFail(w, http.StatusNotFound, "记录不存在")
		return
	}
	if err := a.st.SetArchiveFav(req.JobID, req.Fav); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.logf("收藏变更 user=%s job=%s fav=%v", u.UserID, req.JobID, req.Fav)
	writeOK(w, map[string]bool{"fav": req.Fav})
}

// handleHistoryImage 历史取图(归档双模: OSS 302 / 本地文件; jobs 清理后仍可用)。
func (a *API) handleHistoryImage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	row, err := a.st.Archive(r.PathValue("id"))
	if err != nil || row == nil || row.UserID != u.UserID {
		writeFail(w, http.StatusNotFound, "记录不存在")
		return
	}
	if row.OssKey != "" {
		// ?thumb=1 预览缩略图(560宽q80, 实测原图1.4MB→57KB); 召回/下载不带参数走全图
		sign := presignOSSGET
		if r.URL.Query().Get("thumb") == "1" {
			sign = presignOSSGETThumb
		}
		if signed, err := sign(row.OssKey, 15*time.Minute); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, signed, http.StatusFound)
			return
		} else {
			a.logf("历史 OSS 预签名失败, 回退本地文件: %v", err)
		}
	}
	if row.ImagePath == "" {
		writeFail(w, http.StatusNotFound, "图片不存在")
		return
	}
	serveFile(w, row.ImagePath)
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
	jobID := "pg-" + randomHex(8)
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
	if err != nil || p == nil || !p.Ready || (p.ImagePath == "" && p.OssKey == "") {
		writeFail(w, http.StatusNotFound, "暂无预生成成品")
		return
	}
	// 提货即归档: pregen 成品被取走=进入用户历史(未提货的不算)
	if err := a.st.ArchivePregen(*p); err != nil {
		a.logf("pregen 归档失败 job=%s: %v", p.JobID, err)
	}
	if err := a.st.RecordSiteEvent("img_dl", u.UserID, siteVisitorIP(r)); err != nil {
		a.logf("下载图计数失败: %v", err)
	}
	// 双模: 图片在 OSS → 302 签名 URL 直下(提货即删语义不变, 行已弹出)
	if p.OssKey != "" {
		if u, err := presignOSSGET(p.OssKey, 15*time.Minute); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, u, http.StatusFound)
			return
		} else {
			a.logf("OSS 预签名失败, 回退本地文件: %v", err)
		}
	}
	serveFile(w, p.ImagePath)
}
