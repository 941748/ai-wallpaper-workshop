package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wallpaper/cloud/store"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// ---------- 注册 / 健康 ----------

// deviceInfoPayload register/link 可选携带的设备信息(安卓首启上报; 旧客户端不带则仅登记设备名)。
type deviceInfoPayload struct {
	Type         string `json:"type"` // pc / android
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	OS         string `json:"os"`
	OSVersion  string `json:"os_version"`
	AppVersion string `json:"app_version"`
	ScreenW    int    `json:"screen_w"`
	ScreenH    int    `json:"screen_h"`
}

// registerDevice 登记设备: 有 device_info 走属性 upsert, 否则保持旧调用(仅设备名+活跃)。
func (a *API) registerDevice(device, userID string, info *deviceInfoPayload) {
	if device == "" {
		return
	}
	if info == nil {
		_ = a.st.AddDevice(device, userID)
		return
	}
	_ = a.st.UpsertDeviceAttrs(store.DeviceAttr{
		Device: device, UserID: userID,
		Type: info.Type, Model: info.Model, OS: info.OS, OSVersion: info.OSVersion,
		Manufacturer: info.Manufacturer,
		AppVer: info.AppVersion, ScreenW: info.ScreenW, ScreenH: info.ScreenH,
	})
}

func (a *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device     string             `json:"device"`
		DeviceInfo *deviceInfoPayload `json:"device_info"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	userID := "u_" + randomHex(8)
	token := randomHex(24)
	if err := a.st.CreateUser(userID, token); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.registerDevice(body.Device, userID, body.DeviceInfo)
	a.ensureDefaultSnapshot(userID)
	a.logf("注册新用户 %s (device=%s)", userID, body.Device)
	writeOK(w, map[string]string{"user_id": userID, "token": token})
}

// ensureDefaultSnapshot 全新用户画像快照默认落风格包(与 PC FirstRunSetup 同语义)。
// 安卓无本地画像, 首装全靠本快照下发; 仅快照不存在时写入, 存量用户不受影响。
func (a *API) ensureDefaultSnapshot(userID string) {
	if snap, err := a.st.LoadProfileSnapshot(userID); err != nil || snap != nil {
		return
	}
	packs, err := json.Marshal([]string{taxonomy.DefaultPackName})
	if err != nil {
		return
	}
	_ = a.st.SaveProfileSnapshot(store.ProfileSnapshot{
		UserID: userID, ProfileVersion: 1, Packs: string(packs),
	})
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	quotaOK := true
	if u := a.authUser(r); u != nil {
		quotaOK = a.checkQuota(u, "gen") && a.checkQuota(u, "llm")
	}
	// 云渲染可用性 = 出图机代理在线(S3: 代理领任务渲染, 无代理即无法出图)。
	renderOK := false
	if ts := a.agentSeen.Load(); ts > 0 {
		renderOK = time.Since(time.Unix(ts, 0)) < 10*time.Minute
	}
	writeOK(w, map[string]any{"ok": true, "quota_ok": quotaOK, "render_ok": renderOK,
		"server_version": a.version, "agent_version": a.agentVersion()})
}

// ---------- 设备扫码配对 ----------

func (a *API) handleLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action     string             `json:"action"`
		Code       string             `json:"code"`
		Device     string             `json:"device"`
		DeviceInfo *deviceInfoPayload `json:"device_info"`
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
		// 真"更换": 请求方(带旧身份token)在旧身份下的设备行迁走——否则旧身份残留孤儿行
		// (负责人 2026-11-12: "把B的userid更换成所加入的A的userid")。
		if old := a.authUser(r); old != nil && old.UserID != u.UserID && body.Device != "" {
			_, _ = a.st.DeleteDevice(old.UserID, body.Device)
		}
		if body.Device != "" {
			a.registerDevice(body.Device, u.UserID, body.DeviceInfo)
		}
		a.logf("设备配对成功 user=%s device=%s", u.UserID, body.Device)
		writeOK(w, map[string]string{"user_id": u.UserID, "token": u.Token})
	default:
		writeFail(w, http.StatusBadRequest, "未知 action")
	}
}

// handleDeviceRefresh POST /api/v1/devices/refresh 刷新本机设备属性(厂商/型号/屏幕)。
// 存量设备行只在注册/配对时报过 device_info, 覆盖安装/老客户端不再重报——
// 双端看设备列表前调一次, 保证厂商型号立即可见(负责人的 PC/P30 存量行不显示问题)。
func (a *API) handleDeviceRefresh(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Device     string             `json:"device"`
		DeviceInfo *deviceInfoPayload `json:"device_info"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if body.Device == "" || body.DeviceInfo == nil {
		writeOK(w, map[string]bool{"ok": false}) // 无信息静默成功, 不阻塞列表
		return
	}
	a.registerDevice(body.Device, u.UserID, body.DeviceInfo)
	writeOK(w, map[string]bool{"ok": true})
}

// handleDeviceDelete DELETE /api/v1/devices/{device} 删除配对设备行(仅本人身份下)。
// 用途: PC 端清理卸载重装后产生的重复设备条目(安卓端不提供删除入口, 负责人 2026-11-12 定)。
func (a *API) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	device := r.PathValue("device")
	if device == "" {
		writeFail(w, http.StatusBadRequest, "缺少设备标识")
		return
	}
	found, err := a.st.DeleteDevice(u.UserID, device)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeFail(w, http.StatusNotFound, "设备不存在")
		return
	}
	a.logf("删除配对设备 user=%s device=%s", u.UserID, device)
	writeOK(w, map[string]bool{"ok": true})
}

// handleDevices GET /api/v1/devices 已配对设备列表(双端设置页展示)。
// 显示名由客户端按规格决定: 手机=型号(model), PC=机器名(device 列); 配对几个返回几个。
func (a *API) handleDevices(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListDevices(u.UserID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "设备列表读取失败")
		return
	}
	if rows == nil {
		rows = []store.DeviceRow{}
	}
	writeOK(w, map[string]any{"devices": rows})
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

// ---------- 画像下行 ----------

// handleProfile GET /api/v1/profile 画像快照下行(安卓/新设备配对后拉取, 补齐此前仅有上行的缺口)。
// packs 三态原样透传: null=旧档未设置(按权重推断) / []=显式清空 / [...]=选中包。
// screen 属设备维度(方案A)不下发。
func (a *API) handleProfile(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	out := map[string]any{
		"profile_version": 0,
		"profile":         map[string]float64{},
		"disliked":        []string{},
		"custom":          []string{},
		"packs":           nil,
		"paired":          false,
	}
	// 是否已与电脑端配对(存在 PC 设备即视为已配对; 安卓据此隐藏配对框防重复配对)
	if paired, err := a.st.HasPCDevice(u.UserID); err == nil {
		out["paired"] = paired
	}
	snap, err := a.st.LoadProfileSnapshot(u.UserID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "画像读取失败")
		return
	}
	if snap != nil {
		out["profile_version"] = snap.ProfileVersion
		if snap.Profile != "" {
			var m map[string]float64
			if json.Unmarshal([]byte(snap.Profile), &m) == nil && m != nil {
				out["profile"] = m
			}
		}
		if snap.Disliked != "" {
			var arr []string
			if json.Unmarshal([]byte(snap.Disliked), &arr) == nil && arr != nil {
				out["disliked"] = arr
			}
		}
		if snap.Custom != "" {
			var arr []string
			if json.Unmarshal([]byte(snap.Custom), &arr) == nil && arr != nil {
				out["custom"] = arr
			}
		}
		if snap.Packs != "" {
			var arr []string
			if json.Unmarshal([]byte(snap.Packs), &arr) == nil && arr != nil {
				out["packs"] = arr
			}
		}
	}

	// 词条中文标签: 覆盖 profile/disliked/custom 全部已选 key(安卓即时中文显示)
	labels := map[string]string{}
	collectLabels := func(keys []string) {
		for _, k := range keys {
			if dim, val, ok := strings.Cut(k, "/"); ok {
				if cn := cnLabel(dim, val); cn != "" && cn != val {
					labels[k] = cn
				} else if dim != "" && val != "" {
					labels[k] = val
				}
			}
		}
	}
	if m, ok := out["profile"].(map[string]float64); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		collectLabels(keys)
	}
	if arr, ok := out["disliked"].([]string); ok {
		collectLabels(arr)
	}
	if arr, ok := out["custom"].([]string); ok {
		collectLabels(arr)
	}
	out["labels"] = labels

	// 完整词典(供安卓点选编辑 UI): 5 维全部词条 + 风格包覆盖关系
	dims := make([]map[string]any, 0, len(taxonomy.Dimensions))
	for _, d := range taxonomy.Dimensions {
		vals := make([]map[string]string, 0, len(d.Values))
		for _, v := range d.Values {
			vals = append(vals, map[string]string{"id": v.ID, "name": v.NameCN})
		}
		dims = append(dims, map[string]any{"id": d.ID, "name": d.NameCN, "values": vals})
	}
	dictPacks := make([]map[string]any, 0, len(taxonomy.PresetPacks))
	for _, p := range taxonomy.PresetPacks {
		covers := make([]string, 0, 16)
		for _, d := range taxonomy.Dimensions {
			for _, v := range p.Values(d.ID) {
				covers = append(covers, d.ID+"/"+v)
			}
		}
		dictPacks = append(dictPacks, map[string]any{"name": p.Name, "covers": covers})
	}
	out["dict"] = map[string]any{"dimensions": dims, "packs": dictPacks}

	writeOK(w, out)
}

// ---------- 画像编辑推送(安卓点选编辑 → 双端同步) ----------

// handleProfileSave POST /api/v1/profile 安卓编辑后的完整画像推送。
// 语义: pv 由服务端授版(当前+1), 保证"新者胜"; packs/disliked/custom 用指针区分
// "省略=保持现值" 与 "空数组=显式清空"; 校验 ≥1 包或 ≥1 喜欢(与 PC 同规则, 防清空乱出图)。
func (a *API) handleProfileSave(w http.ResponseWriter, r *http.Request) {
	u, ok := a.requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Profile  map[string]float64 `json:"profile"`
		Disliked *[]string          `json:"disliked"`
		Custom   *[]string          `json:"custom"`
		Packs    *[]string          `json:"packs"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeFail(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	packsSelected := req.Packs != nil && len(*req.Packs) > 0
	hasLike := false
	for _, v := range req.Profile {
		if v >= survey.LikeWeight-1e-9 {
			hasLike = true
			break
		}
	}
	if !packsSelected && !hasLike {
		writeFail(w, http.StatusBadRequest, "至少保留一个风格包或一个喜欢的标签")
		return
	}

	old, _ := a.st.LoadProfileSnapshot(u.UserID)
	newPv, screenW, screenH := 1, 0, 0
	if old != nil {
		newPv = old.ProfileVersion + 1
		screenW, screenH = old.ScreenW, old.ScreenH // screen 属设备维度, 编辑推送不动它
	}
	snap := store.ProfileSnapshot{
		UserID: u.UserID, ProfileVersion: newPv, ScreenW: screenW, ScreenH: screenH,
	}
	if b, err := json.Marshal(req.Profile); err == nil {
		snap.Profile = string(b)
	}
	if req.Disliked != nil {
		if b, err := json.Marshal(*req.Disliked); err == nil {
			snap.Disliked = string(b)
		}
	} else if old != nil {
		snap.Disliked = old.Disliked
	}
	if req.Custom != nil {
		if b, err := json.Marshal(*req.Custom); err == nil {
			snap.Custom = string(b)
		}
	} else if old != nil {
		snap.Custom = old.Custom
	}
	if req.Packs != nil {
		if b, err := json.Marshal(*req.Packs); err == nil {
			snap.Packs = string(b)
		}
	} else if old != nil {
		snap.Packs = old.Packs // 省略 = 保持三态现值
	}
	if err := a.st.SaveProfileSnapshot(snap); err != nil {
		writeFail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	a.logf("画像编辑推送 user=%s pv=%d packs=%v", u.UserID, newPv, req.Packs)
	writeOK(w, map[string]any{"profile_version": newPv})
}
