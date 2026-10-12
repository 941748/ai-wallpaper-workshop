package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	// 出图机可达性(服务端短超时探测+缓存): 客户端据此在首张壁纸前感知出图链路。
	renderOK := false
	if a.q != nil && a.q.comfy != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		renderOK = a.q.comfy.Reachable(ctx)
		cancel()
	}
	writeOK(w, map[string]any{"ok": true, "quota_ok": quotaOK, "render_ok": renderOK, "server_version": a.version})
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
