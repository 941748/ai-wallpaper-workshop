package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"wallpaper/cloud/store"
)

// ---------- 出图机上传代理协议(P1) ----------
// 代理(aw-uploader, 跑在出图机本机)循环:
//   POST /api/v1/agent/pending  → 拉取 oss_pending 待传清单(服务端指定 oss_key)
//   本机 Comfy /view 取字节 → PUT OSS →
//   POST /api/v1/agent/done     → 服务端标 done(pregen 同步 ready+oss_key)
// 鉴权: AW_AGENT_TOKEN(与代理配置同源; 未配置=503 禁用)。

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
	return true
}

// handleAgentPending 拉取待传清单。
func (a *API) handleAgentPending(w http.ResponseWriter, r *http.Request) {
	if !a.authAgent(w, r) {
		return
	}
	tasks, err := a.st.ListOssPending()
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, map[string]string{
			"job_id":    t.JobID,
			"oss_key":   t.OssKey,
			"filename":  t.ImageName,
			"subfolder": t.ImageSub,
		})
	}
	writeOK(w, map[string]any{"tasks": out})
}

// handleAgentDone 代理上报完成(oss_key 由服务端按 job 重算, 不信客户端值)。
func (a *API) handleAgentDone(w http.ResponseWriter, r *http.Request) {
	if !a.authAgent(w, r) {
		return
	}
	var body struct {
		JobID string `json:"job_id"`
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
	if job.Status != "oss_pending" {
		// 幂等: 已 done(重复上报)直接成功
		writeOK(w, map[string]bool{"ok": true})
		return
	}
	ossKey := store.OssKeyFor(job.UserID, job.JobID)
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
	a.logf("代理上传确认 job=%s kind=%s -> %s", job.JobID, job.Kind, ossKey)
	writeOK(w, map[string]bool{"ok": true})
}
