package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/pool"
	"wallpaper/internal/signals"
	"wallpaper/internal/tick"
)

// TestClientTickAgainstRealCloud 跨模块端到端:
// 真实 tick.RunOnce(注入桌面桩) ↔ 真实云服务(httptest) ↔ mock ComfyUI/LLM。
// 覆盖: 注册 → health → 策略同步 → prompts/next → generate → 轮询 → 下载 → 换壁纸 → 备用池补货/提货/命中 → 上报。
func TestClientTickAgainstRealCloud(t *testing.T) {
	llmMock := newMockLLM(t)
	defer llmMock.Close()
	ts := newTestServer(t, llmMock.URL, true)
	defer ts.srv.Close()

	userID, token := ts.register(t)

	// 客户端数据目录
	clientDir := t.TempDir()
	if err := config.EnsureSubdirs(clientDir); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Initialized = true
	cfg.UserID = userID
	cfg.Token = token
	cfg.CloudURL = ts.srv.URL
	cfg.ProfileVer = 1
	cfg.TaskTickHours = 1 // 视为已修正, 测试中不触碰系统计划任务
	cfg.Satisfaction.IntervalDays = 3
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)
	cfg.Satisfaction.NextAskAt = now.Add(48 * time.Hour).Format(time.RFC3339) // 未到期
	if err := cfg.Save(clientDir); err != nil {
		t.Fatal(err)
	}
	profile := config.NewProfile()
	profile.Set("style", "ink", 0.9)
	if err := profile.Save(clientDir); err != nil {
		t.Fatal(err)
	}

	var wpPath string
	env := tick.Env{
		Dir:        clientDir,
		Now:        func() time.Time { return now },
		Screen:     func() (int, int) { return 1920, 1080 },
		Quiet:      func() bool { return false },
		SetWP:      func(p string) error { wpPath = p; return nil },
		ExePath:    filepath.Join(clientDir, "bin", "wallpaper.exe"), // 更新检查指向不存在文件, 无更新时不会用到
		SelfUpdate: true,                                              // 顺带走一遍 /client/latest
	}

	// 第一轮: 现取(LLM)出图
	if err := tick.RunOnce(context.Background(), env); err != nil {
		t.Fatalf("round1: %v", err)
	}
	if wpPath == "" {
		t.Fatal("round1: wallpaper not applied")
	}
	if _, err := os.Stat(wpPath); err != nil {
		t.Fatalf("round1: wallpaper file missing: %v", err)
	}

	// 云端侧记录验证
	prompts, err := ts.st.AdminRecentPrompts(5)
	if err != nil || len(prompts) == 0 {
		t.Fatalf("no prompt records on cloud: %v", err)
	}
	if prompts[0].Source != "cloud" || !prompts[0].Success {
		t.Fatalf("round1 record unexpected: %+v", prompts[0])
	}
	if prompts[0].UserID != userID {
		t.Fatalf("record user mismatch: %s", prompts[0].UserID)
	}

	// 预生成已下单
	if p, _ := ts.st.GetPregen(userID); p == nil {
		t.Fatal("pregen not submitted")
	}
	// 等待预生成就绪(idle 队列, 测试中延迟 1ms)
	deadline := time.Now().Add(10 * time.Second)
	pregenReady := false
	for time.Now().Before(deadline) {
		if p, _ := ts.st.GetPregen(userID); p != nil && p.Ready {
			pregenReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !pregenReady {
		t.Fatal("pregen not ready")
	}

	// 第二轮: 现取出图 + 在途预生成提货入本地备用池(12:00 已出活跃时段, 推进到 11:00 下一小时)
	now = now.Add(1 * time.Hour)
	wpPath = ""
	if err := tick.RunOnce(context.Background(), env); err != nil {
		t.Fatalf("round2: %v", err)
	}
	if wpPath == "" {
		t.Fatal("round2: wallpaper not applied")
	}
	prompts, _ = ts.st.AdminRecentPrompts(5)
	if prompts[0].Source != "cloud" {
		t.Fatalf("round2 should be realtime, got source=%s", prompts[0].Source)
	}
	if pool.Count(clientDir) != 1 {
		t.Fatalf("round2: pool count=%d want 1", pool.Count(clientDir))
	}
	if pool.LoadPending(clientDir) != nil {
		t.Fatal("round2: pending should be cleared after delivery")
	}

	// 第二轮补: 备用池命中(秒换, 不再请求云端出图)
	now = now.Add(3*time.Hour + 30*time.Minute)
	wpPath = ""
	if err := tick.RunOnce(context.Background(), env); err != nil {
		t.Fatalf("round2b: %v", err)
	}
	if wpPath == "" {
		t.Fatal("round2b: wallpaper not applied")
	}
	prompts, _ = ts.st.AdminRecentPrompts(5)
	if prompts[0].Source != "pool" {
		t.Fatalf("round2b should hit pool, got source=%s", prompts[0].Source)
	}
	if pool.Count(clientDir) != 0 {
		t.Fatalf("round2b: pool should be consumed, count=%d", pool.Count(clientDir))
	}

	// 第三轮: 满意度回访"换个风格"到期(注意与 env.Now 用同一时间基准)
	now = now.Add(1*time.Hour + 30*time.Minute)
	cfg2, _ := config.Load(clientDir)
	cfg2.Satisfaction.NextAskAt = now.Add(-time.Minute).Format(time.RFC3339)
	_ = cfg2.Save(clientDir)
	env.Ask = func(tick.AskRequest) tick.AskChoice { return tick.ChoiceStyle }
	wpPath = ""
	if err := tick.RunOnce(context.Background(), env); err != nil {
		t.Fatalf("round3: %v", err)
	}
	if wpPath == "" {
		t.Fatal("round3: wallpaper not applied")
	}
	// style_reject 已上报并入库
	sigs, err := ts.st.AdminRecentSignals(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sigs {
		if s.UserID == userID && s.Type == signals.TypeStyleReject {
			found = true
		}
	}
	if !found {
		t.Fatalf("style_reject not stored: %+v", sigs)
	}
	// 回访周期重置为 3 天
	cfg3, _ := config.Load(clientDir)
	if cfg3.Satisfaction.IntervalDays != 3 {
		t.Fatalf("interval should reset to 3, got %d", cfg3.Satisfaction.IntervalDays)
	}

	// 第四轮: 云端发布了一个 sha256 错误的"新版本" → 自更新必须失败,
	// 但绝不中断主线: 本轮壁纸仍要正常更换, 且错误版本不得暂存。
	now = now.Add(1 * time.Hour)
	relDir := filepath.Join(ts.data, "releases")
	if err := os.WriteFile(filepath.Join(relDir, "wallpaper-9.9.9.exe"), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"9.9.9","file":"wallpaper-9.9.9.exe","sha256":"` +
		strings.Repeat("00", 32) + `","size":9,"notes":"bad sha test"}`
	if err := os.WriteFile(filepath.Join(relDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	wpPath = ""
	if err := tick.RunOnce(context.Background(), env); err != nil {
		t.Fatalf("round4: %v", err)
	}
	if wpPath == "" {
		t.Fatal("round4: 自更新失败不能影响本轮换壁纸")
	}
	if _, err := os.Stat(filepath.Join(clientDir, "bin", "wallpaper.new.exe")); !os.IsNotExist(err) {
		t.Fatal("round4: sha256 校验失败的版本不得暂存")
	}

	// 漂移摘要包含"换风格"
	drift, _ := ts.st.GetDrift(userID)
	if drift == "" || !strings.Contains(drift, "换风格") {
		t.Fatalf("drift summary missing: %q", drift)
	}
}
