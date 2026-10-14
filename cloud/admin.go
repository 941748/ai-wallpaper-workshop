package main

import (
	"html/template"
	"net/http"
	"sort"
)

// ---------- 管理看板 ----------

var adminTmpl = template.Must(template.New("admin").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>{{.App}} 管理看板</title>
<style>body{font-family:system-ui;margin:24px;background:#0f172a;color:#e2e8f0}
table{border-collapse:collapse;width:100%;margin:12px 0}td,th{border:1px solid #334155;padding:6px 10px;font-size:13px;text-align:left}
th{background:#1e293b}h2{margin-top:28px}code{color:#7dd3fc}</style></head><body>
<h1>{{.App}} 云服务 · v{{.Version}}</h1>
<h2>核心指标</h2>
<p>出图总数 <b>{{.GenTotal}}</b> · 成功 <b>{{.GenOK}}</b> · 图片下载 <b>{{.SiteImgDL}}</b> · 官网浏览 <b>{{.SitePV}}</b> · 官网访客 <b>{{.SiteUV}}</b> · 客户端下载 <b>{{.SiteDL}}</b></p>
<h2>按天明细 (近 14 天)</h2>
<table><tr><th>日期</th><th>出图</th><th>成功</th><th>图片下载</th><th>官网PV</th><th>官网UV</th><th>客户端下载</th></tr>
{{range .DailyRows}}<tr><td>{{.Day}}</td><td>{{.Gen}}</td><td>{{.GenOK}}</td><td>{{.ImgDL}}</td><td>{{.PV}}</td><td>{{.UV}}</td><td>{{.DL}}</td></tr>{{end}}</table>
<h2>最近访问事件 (20, 含 IP)</h2>
<table><tr><th>时间</th><th>类型</th><th>IP</th><th>标识(哈希/user)</th></tr>
{{range .SiteEvents}}<tr><td>{{.TS}}</td><td>{{.Kind}}</td><td>{{.IP}}</td><td><code>{{.IPh}}</code></td></tr>{{end}}</table>
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

type dailyRow struct {
	Day           string
	Gen, GenOK    int64
	ImgDL, PV, UV int64
	DL            int64
}

// mergeDaily 合并出图与官网两个数据源的按天统计(以日期为键, 结果按日期倒序)。
func mergeDaily(site []siteDaily, gen []genDayRow) []dailyRow {
	m := map[string]*dailyRow{}
	for _, g := range gen {
		m[g.Day] = &dailyRow{Day: g.Day, Gen: g.Total, GenOK: g.Success}
	}
	for _, s := range site {
		if r, ok := m[s.Day]; ok {
			r.PV, r.UV, r.DL, r.ImgDL = s.PV, s.UV, s.DL, s.ImgDL
		} else {
			m[s.Day] = &dailyRow{Day: s.Day, PV: s.PV, UV: s.UV, DL: s.DL, ImgDL: s.ImgDL}
		}
	}
	out := make([]dailyRow, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out
}

type siteDaily struct {
	Day           string
	PV, UV, DL    int64
	ImgDL         int64
}
type genDayRow struct {
	Day     string
	Total   int64
	Success int64
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

	var siteRows []siteDaily
	var genRows []genDayRow
	if pv, uv, dl, imgDL, daily, err := a.st.SiteStats(14); err == nil {
		data["SitePV"], data["SiteUV"], data["SiteDL"], data["SiteImgDL"] = pv, uv, dl, imgDL
		for _, d := range daily {
			siteRows = append(siteRows, siteDaily{d.Day, d.PV, d.UV, d.DL, d.ImgDL})
		}
	}
	if genTotal, genOK, genDaily, err := a.st.GenStats(14); err == nil {
		data["GenTotal"], data["GenOK"] = genTotal, genOK
		for _, d := range genDaily {
			genRows = append(genRows, genDayRow{d.Day, d.Total, d.Success})
		}
	}
	data["DailyRows"] = mergeDaily(siteRows, genRows)

	if evs, err := a.st.AdminRecentSiteEvents(20); err == nil {
		data["SiteEvents"] = evs
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
