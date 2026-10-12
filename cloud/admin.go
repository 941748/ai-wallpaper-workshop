package main

import (
	"html/template"
	"net/http"
)

// ---------- 管理看板 ----------

var adminTmpl = template.Must(template.New("admin").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>{{.App}} 管理看板</title>
<style>body{font-family:system-ui;margin:24px;background:#0f172a;color:#e2e8f0}
table{border-collapse:collapse;width:100%;margin:12px 0}td,th{border:1px solid #334155;padding:6px 10px;font-size:13px;text-align:left}
th{background:#1e293b}h2{margin-top:28px}code{color:#7dd3fc}</style></head><body>
<h1>{{.App}} 云服务 · v{{.Version}}</h1>
<h2>官网统计</h2>
<p>总浏览 <b>{{.SitePV}}</b> · 总访客 <b>{{.SiteUV}}</b> · 客户端下载 <b>{{.SiteDL}}</b></p>
<table><tr><th>日期</th><th>浏览(PV)</th><th>访客(UV)</th><th>下载</th></tr>
{{range .SiteDaily}}<tr><td>{{.Day}}</td><td>{{.PV}}</td><td>{{.UV}}</td><td>{{.DL}}</td></tr>{{end}}</table>
<h2>用户 ({{len .Users}})</h2>
<table><tr><th>user_id</th><th>注册时间</th><th>今日 LLM</th><th>今日出图</th><th>禁用</th></tr>
{{range .Users}}<tr><td><code>{{.UserID}}</code></td><td>{{.CreatedAt}}</td><td>{{.LLM}}</td><td>{{.Gen}}</td><td>{{.Disabled}}</td></tr>{{end}}</table>
<h2>最近出图记录 (20)</h2>
<table><tr><th>时间</th><th>user</th><th>来源</th><th>工作流</th><th>尺寸</th><th>成功</th><th>提示词(截断)</th></tr>
{{range .Prompts}}<tr><td>{{.At}}</td><td><code>{{.User}}</code></td><td>{{.Source}}</td><td>{{.WorkflowID}}</td><td>{{.W}}x{{.H}}</td><td>{{.Success}}</td><td>{{.Positive}}</td></tr>{{end}}</table>
<h2>最近漂移事件 (30)</h2>
<table><tr><th>时间</th><th>user</th><th>类型</th><th>详情</th></tr>
{{range .Signals}}<tr><td>{{.At}}</td><td><code>{{.User}}</code></td><td>{{.Type}}</td><td>{{.Detail}}</td></tr>{{end}}</table>
<h2>任务队列</h2>
<table><tr><th>job_id</th><th>kind</th><th>priority</th><th>status</th><th>error</th></tr>
{{range .Jobs}}<tr><td><code>{{.JobID}}</code></td><td>{{.Kind}}</td><td>{{.Priority}}</td><td>{{.Status}}</td><td>{{.Error}}</td></tr>{{end}}</table>
</body></html>`))

type adminUserRow struct {
	UserID, CreatedAt string
	LLM, Gen          int
	Disabled          bool
}

func (a *API) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if a.adminToken == "" || r.URL.Query().Get("token") != a.adminToken {
		http.NotFound(w, r)
		return
	}
	users, _ := a.st.AllUsers()
	var userRows []adminUserRow
	for _, u := range users {
		llmN, genN, _ := a.st.UsageToday(u.UserID)
		userRows = append(userRows, adminUserRow{u.UserID, u.CreatedAt, llmN, genN, u.Disabled})
	}
	data := map[string]any{"App": appNameCN, "Version": a.version, "Users": userRows}
	if pv, uv, dl, daily, err := a.st.SiteStats(14); err == nil {
		data["SitePV"], data["SiteUV"], data["SiteDL"], data["SiteDaily"] = pv, uv, dl, daily
	}

	// 最近记录/信号/任务: 通过 db 直查简化(只读)
	type pr struct {
		At, User, Source, WorkflowID, Positive string
		W, H                                   int
		Success                                bool
	}
	var prompts []pr
	if rows, err := a.st.AdminRecentPrompts(20); err == nil {
		for _, x := range rows {
			pos := x.Positive
			if len(pos) > 80 {
				pos = pos[:80] + "…"
			}
			prompts = append(prompts, pr{x.At, x.UserID, x.Source, x.WorkflowID, pos, x.Width, x.Height, x.Success})
		}
	}
	data["Prompts"] = prompts

	type sg struct{ At, User, Type, Detail string }
	var sigs []sg
	if rows, err := a.st.AdminRecentSignals(30); err == nil {
		for _, x := range rows {
			sigs = append(sigs, sg{x.At, x.UserID, x.Type, x.Detail})
		}
	}
	data["Signals"] = sigs

	if jobs, err := a.st.AdminRecentJobs(15); err == nil {
		data["Jobs"] = jobs
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.Execute(w, data)
}
