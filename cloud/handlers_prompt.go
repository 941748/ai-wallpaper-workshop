package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"wallpaper/cloud/store"
)

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
	a.saveProfileSnapshot(u.UserID, req)
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
