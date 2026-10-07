// AI 壁纸工坊云服务: 统一代理出图与 LLM 调用(计量/配额/限流/远程禁用),
// 存储提示词历史与漂移事件, 空闲预生成队列, 客户端静默更新分发, 设备扫码配对。
// 可部署于任意服务器(见 README 部署章节); SQLite 单文件; LLM 密钥仅存服务端。
package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"wallpaper/cloud/llm"
	"wallpaper/cloud/store"
)

const serverVersion = "0.1.0"
const appNameCN = "AI 壁纸工坊"

func main() {
	log.SetFlags(log.LstdFlags)
	logf := func(format string, args ...any) { log.Printf(format, args...) }

	dataDir := env("AW_DATA", "./clouddata")
	for _, sub := range []string{"probes", "images", "pregen", "releases"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			log.Fatalf("数据目录创建失败: %v", err)
		}
	}

	st, err := store.Open(filepath.Join(dataDir, "cloud.db"))
	if err != nil {
		log.Fatalf("数据库打开失败: %v", err)
	}
	defer st.Close()

	reg := LoadRegistry(filepath.Join(dataDir, "workflows.json"))

	llmClient := llm.New(env("AW_LLM_BASE", "https://token-plan-cn.xiaomimimo.com/v1"), env("AW_LLM_KEY", ""), env("AW_LLM_MODEL", "mimo-v2.6-flash"))
	if !llmClient.Enabled() {
		logf("警告: 未配置 AW_LLM_KEY, LLM 决策将回退本地引擎")
	}

	comfy := NewComfy(env("AW_COMFY_BASE", "http://127.0.0.1:8188"), env("AW_COMFY_TOKEN", ""))

	q := &queue{st: st, comfy: comfy, reg: reg, dataDir: dataDir, logf: logf}
	q.ensureProbePool() // 公共探针池: 缺图自动排入空闲队列

	api := &API{
		st: st, llm: llmClient, reg: reg, q: q, dataDir: dataDir,
		siteDir:    env("AW_SITE", "/opt/aiwallpaper/site"),
		dlDir:      env("AW_DL", "/opt/aiwallpaper/downloads"),
		adminToken: env("AW_ADMIN_TOKEN", ""),
		version:    serverVersion,
		logf:       logf,
		genHour:    envInt("AW_QUOTA_GEN_HOUR", 6),
		genDay:     envInt("AW_QUOTA_GEN_DAY", 40),
		llmHour:    envInt("AW_QUOTA_LLM_HOUR", 8),
		llmDay:     envInt("AW_QUOTA_LLM_DAY", 60),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 队列 worker
	go q.Run(ctx)
	// 周期维护: 清理旧任务与过期票据
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = st.PurgeJobs(time.Now().AddDate(0, 0, -7))
			}
		}
	}()

	addr := env("AW_ADDR", ":8707")
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logf("%s 云服务已启动 %s (data=%s, comfy=%s)", appNameCN, addr, dataDir, comfy.Base)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	// 优雅退出
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	cancel()
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = srv.Shutdown(shutdownCtx)
	logf("已退出")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ---------- 管理看板 ----------

var adminTmpl = template.Must(template.New("admin").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>{{.App}} 管理看板</title>
<style>body{font-family:system-ui;margin:24px;background:#0f172a;color:#e2e8f0}
table{border-collapse:collapse;width:100%;margin:12px 0}td,th{border:1px solid #334155;padding:6px 10px;font-size:13px;text-align:left}
th{background:#1e293b}h2{margin-top:28px}code{color:#7dd3fc}</style></head><body>
<h1>{{.App}} 云服务 · v{{.Version}}</h1>
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
