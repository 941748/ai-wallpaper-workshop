package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"wallpaper/cloud/store"
)

// ---------- 出图机统一代理协议(S3: 调度+上传合并, 替代 frpc 反向隧道) ----------
// 代理(aw-agent, 跑在出图机本机)循环:
//   POST /api/v1/agent/claim     → 领取渲染任务(服务端下发正反提示词/尺寸/seed/工作流/OSS键)
//   本地模板占位符注入 → 本机 ComfyUI 出图 → JPEG 转码 → PUT OSS →
//   POST /api/v1/agent/rendered  → 回报(成功标 done+归档; 失败标 failed)
// 鉴权: AW_AGENT_TOKEN(恒时比对; 未配置=503 禁用)。任意一次代理请求刷新在线时间(供看板/health)。

func (a *API) authAgent(w http.ResponseWriter, r *http.Request) bool {
	tok := os.Getenv("AW_AGENT_TOKEN")
	if tok == "" {
		writeFail(w, http.StatusServiceUnavailable, "agent 未启用")
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeFail(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	a.agentSeen.Store(time.Now().Unix())
	return true
}

// handleAgentClaim 领取渲染任务: normal 优先, 空了才给 idle(预生成)。
// 单 GPU 串行渲染, 每次最多 1 个; 无任务返回 task=null(代理继续轮询)。
func (a *API) handleAgentClaim(w http.ResponseWriter, r *http.Request) {
	if !a.authAgent(w, r) {
		return
	}
	var body struct {
		Version string `json:"version"` // 代理自报版本(供看板/health 展示)
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if body.Version != "" {
		a.agentVer.Store(body.Version)
	}
	job, err := a.st.ClaimNextJob()
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job == nil {
		writeOK(w, map[string]any{"task": nil})
		return
	}
	// P5: 下发尺寸=物理交付尺寸(全链路物理口径); 渲染限宽由出图机按自身 max_side 自行等比计算,
	// 渲染后放大回物理再上传(渲染限宽职责在出图机: 显存是它自己的资源)。
	ossKey := store.OssKeyFor(job.UserID, job.JobID, job.Width, job.Height)
	a.logf("代理领任务 job=%s kind=%s 物理%dx%d wf=%s",
		job.JobID, job.Kind, job.Width, job.Height, job.WorkflowID)
	writeOK(w, map[string]any{"task": map[string]any{
		"job_id":       job.JobID,
		"kind":         job.Kind,
		"positive":     job.Positive,
		"negative":     job.Negative,
		"width":        job.Width,
		"height":       job.Height,
		"seed":         job.Seed,
		"workflow_id":  job.WorkflowID,
		"oss_key":      ossKey, // 服务端算键(物理尺寸), 代理只负责传到位
	}})
}

// handleAgentRendered 回报渲染结果(oss_key 由服务端重算核对, 不信客户端值)。
func (a *API) handleAgentRendered(w http.ResponseWriter, r *http.Request) {
	if !a.authAgent(w, r) {
		return
	}
	var body struct {
		JobID  string `json:"job_id"`
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		OssKey string `json:"oss_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.JobID == "" {
		writeFail(w, http.StatusBadRequest, "bad request")
		return
	}
	job, err := a.st.Job(body.JobID)
	if err != nil || job == nil {
		writeFail(w, http.StatusNotFound, "任务不存在")
		return
	}
	if job.Status == "done" {
		writeOK(w, map[string]bool{"ok": true}) // 幂等: 重复回报直接成功
		return
	}
	if !body.OK {
		msg := body.Error
		if msg == "" {
			msg = "渲染失败"
		}
		_ = a.st.SetJobStatus(job.JobID, "failed", msg, "")
		if job.Kind == "pregen" {
			_ = a.st.DropPregen(job.JobID) // 失败即清, 不虚增在途计数
		}
		a.logf("代理回报失败 job=%s kind=%s: %s", job.JobID, job.Kind, msg)
		writeOK(w, map[string]bool{"ok": true})
		return
	}
	ossKey := store.OssKeyFor(job.UserID, job.JobID, job.Width, job.Height)
	if body.OssKey != "" && body.OssKey != ossKey {
		// 代理传到了错误的键 → 拒绝落 done(下载会404), 任务留给超时回收明确失败
		a.logf("代理 oss_key 不匹配 job=%s: got=%s want=%s", job.JobID, body.OssKey, ossKey)
		writeFail(w, http.StatusConflict, "oss_key 不匹配")
		return
	}
	if err := a.st.SetJobOssKey(job.JobID, ossKey); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.st.SetJobStatus(job.JobID, "done", "", ""); err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job.Kind == "pregen" {
		_ = a.st.SetPregenReady(job.JobID, "")
		_ = a.st.SetPregenOssKey(job.JobID, ossKey)
	} else if err := a.st.ArchiveJob(job.JobID); err != nil {
		a.logf("归档失败 job=%s: %v", job.JobID, err)
	}
	a.logf("代理回报完成 job=%s kind=%s -> %s", job.JobID, job.Kind, ossKey)
	writeOK(w, map[string]bool{"ok": true})
}
