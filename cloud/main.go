// AI 壁纸工坊云服务: 统一代理出图与 LLM 调用(计量/配额/限流/远程禁用),
// 存储提示词历史与漂移事件, 空闲预生成队列, 客户端静默更新分发, 设备扫码配对。
// 可部署于任意服务器(见 README 部署章节); SQLite 单文件; LLM 密钥仅存服务端。
package main

import (
	"context"
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
	"wallpaper/internal/version"
)

// serverVersion 云服务报告的产品版本(与客户端共用单一版本源 internal/version)。
const serverVersion = version.Version
const appNameCN = "AI 壁纸工坊"

func main() {
	log.SetFlags(log.LstdFlags)
	logf := func(format string, args ...any) { log.Printf(format, args...) }

	dataDir := env("AW_DATA", "./clouddata")
	for _, sub := range []string{"images", "pregen", "releases"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			log.Fatalf("数据目录创建失败: %v", err)
		}
	}

	st, err := store.Open(filepath.Join(dataDir, "cloud.db"))
	if err != nil {
		log.Fatalf("数据库打开失败: %v", err)
	}
	defer st.Close()

	// 探针机制已退役: 清理存量 probe 任务, 避免残留任务被 worker 排队消费
	if err := st.PurgeJobsByKind("probe"); err != nil {
		logf("存量探针任务清理失败: %v", err)
	}

	reg := LoadRegistry(filepath.Join(dataDir, "workflows.json"))

	llmClient := llm.New(env("AW_LLM_BASE", "https://token-plan-cn.xiaomimimo.com/v1"), env("AW_LLM_KEY", ""), env("AW_LLM_MODEL", "mimo-v2.6-flash"))
	if !llmClient.Enabled() {
		logf("警告: 未配置 AW_LLM_KEY, LLM 决策将回退本地引擎")
	}

	comfy := NewComfy(env("AW_COMFY_BASE", "http://127.0.0.1:8188"), env("AW_COMFY_TOKEN", ""))

	q := &queue{st: st, comfy: comfy, reg: reg, dataDir: dataDir, logf: logf}

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
	// 空闲主动预生成: 队列 worker 空闲分支周期调用, 为活跃用户保持 ready 库存
	q.idleHook = api.proactivePregen

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
