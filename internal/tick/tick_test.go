package tick

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/policy"
	"wallpaper/internal/pool"
	"wallpaper/internal/signals"
	"wallpaper/internal/taxonomy"
)

// ---------- mock 云端 ----------

type mockCloud struct {
	mu            sync.Mutex
	nextCalls     int
	pregenSubmits int
	pregenFetches int
	forceShake    bool
	prompts       []cloud.PromptRecord
	signalsGot    [][]signals.Event
	pregenReady   bool
	pngData       []byte
	nextFail      bool
	srv           *httptest.Server
}

func writeEnv(w http.ResponseWriter, data any) {
	b, _ := json.Marshal(data)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true,"data":` + string(b) + `}`))
}

func (m *mockCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, map[string]any{"ok": true, "quota_ok": true, "server_version": "test"})
	})
	mux.HandleFunc("/api/v1/prompts/next", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.nextCalls++
		var req cloud.NextReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ForceShake {
			m.forceShake = true
		}
		if m.nextFail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeEnv(w, map[string]any{
			"positive": "cloud positive prompt", "negative": "cloud negative",
			"width": 1344, "height": 768, "workflow_id": "wf-base", "seed": 42,
			"combo": map[string]string{taxonomy.DimStyle: "cyberpunk"}, "reason": "test",
		})
	})
	mux.HandleFunc("/api/v1/prompts", func(w http.ResponseWriter, r *http.Request) {
		var rec cloud.PromptRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		m.mu.Lock()
		m.prompts = append(m.prompts, rec)
		m.mu.Unlock()
		writeEnv(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/signals", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []signals.Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.signalsGot = append(m.signalsGot, body.Events)
		m.mu.Unlock()
		writeEnv(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/generate", func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, map[string]any{"job_id": "job-1"})
	})
	mux.HandleFunc("/api/v1/generate/job-1", func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, map[string]any{"status": "done"})
	})
	mux.HandleFunc("/api/v1/generate/job-1/image", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(m.pngData)
	})
	mux.HandleFunc("/api/v1/pregen", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Method == http.MethodPost {
			m.pregenSubmits++
			writeEnv(w, map[string]any{"queued": true, "job_id": "pg-test"})
			return
		}
		m.pregenFetches++
		writeEnv(w, map[string]any{
			"ready": m.pregenReady, "profile_version": 1,
			"positive": "pregen positive", "negative": "",
			"combo": map[string]string{taxonomy.DimStyle: "ink"}, "seed": 7,
			"workflow_id": "wf-pre", "width": 1344, "height": 768,
		})
	})
	mux.HandleFunc("/api/v1/pregen/image", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(m.pngData)
	})
	mux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, map[string]any{
			"policy_version": 1,
			"active_blocks": [][]string{{"09:00", "12:00"}, {"14:00", "18:00"}, {"20:00", "24:00"}},
			"pool_target": 3, "pause_options_hours": []int{8, 24, 48}, "max_pause_hours": 48,
		})
	})
	mux.HandleFunc("/api/v1/client/latest", func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, map[string]any{"version": "0.1.0"})
	})
	return mux
}

// ---------- 测试环境 ----------

func gradientPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x * y) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type testEnv struct {
	dir       string
	now       time.Time
	wpPath    string
	askChoice AskChoice
	askCalls  int
}

func (te *testEnv) env(cloudURL string, m *mockCloud) Env {
	return Env{
		Dir: te.dir,
		Log: func(format string, args ...any) { tLog(format, args...) },
		Now: func() time.Time { return te.now },
		Screen: func() (int, int) { return 1920, 1080 },
		Quiet:  func() bool { return false },
		SetWP: func(path string) error {
			te.wpPath = path
			return nil
		},
		Ask: func(AskRequest) AskChoice {
			te.askCalls++
			return te.askChoice
		},
		ExePath:    "",
		SelfUpdate: false,
	}
}

var testLogMu sync.Mutex

func tLog(format string, args ...any) {
	testLogMu.Lock()
	defer testLogMu.Unlock()
	// 测试日志静默即可; 需要排查时改为 fmt.Printf
}

func setupClient(t *testing.T, cloudURL string, te *testEnv) {
	t.Helper()
	if err := config.EnsureSubdirs(te.dir); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Initialized = true
	cfg.UserID = "u-test"
	cfg.Token = "tok"
	cfg.CloudURL = cloudURL
	cfg.ProfileVer = 1
	cfg.TaskTickHours = 1 // 视为已修正, 测试中不触碰系统计划任务
	cfg.PhaseMinutes = 17
	cfg.Satisfaction.IntervalDays = 3
	cfg.Satisfaction.NextAskAt = te.now.Add(1 * time.Hour).Format(time.RFC3339) // 未到期
	if err := cfg.Save(te.dir); err != nil {
		t.Fatal(err)
	}
	p := config.NewProfile()
	p.Set(taxonomy.DimStyle, "ink", 0.9)
	p.Set(taxonomy.DimPalette, "cool", 0.8)
	if err := p.Save(te.dir); err != nil {
		t.Fatal(err)
	}
}

// ---------- 测试用例 ----------

func TestRunOnceCloudHappyPath(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768)}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	// 壁纸已应用
	if te.wpPath == "" {
		t.Fatal("wallpaper not set")
	}
	f, err := os.Open(te.wpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1920 || b.Dy() != 1080 {
		t.Fatalf("wallpaper not fitted to screen: %v", b)
	}
	// 云端 LLM 被调用 1 次, 上报 1 条, 预约了下一轮预生成
	if m.nextCalls != 1 {
		t.Fatalf("nextCalls=%d", m.nextCalls)
	}
	if len(m.prompts) != 1 || m.prompts[0].Source != "cloud" || !m.prompts[0].Success {
		t.Fatalf("prompt report wrong: %+v", m.prompts)
	}
	if m.pregenSubmits != 1 {
		t.Fatalf("pregenSubmits=%d", m.pregenSubmits)
	}
	// 历史写入 1 条
	hraw, err := os.ReadFile(filepath.Join(te.dir, "prompt_history.json"))
	if err != nil || len(hraw) == 0 {
		t.Fatalf("history not written: %v", err)
	}
	// 未到期: 不弹回访
	if te.askCalls != 0 {
		t.Fatalf("askCalls=%d", te.askCalls)
	}
}

func TestRunOncePoolFetchDelivers(t *testing.T) {
	// 在途补池单 + 云端已就绪 → 本轮提货入池; 次轮换图走池, 池空后补新单
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768), pregenReady: true}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	if err := pool.SavePending(te.dir, pool.Pending{JobID: "pg-x", ProfileVersion: 1, Since: te.now.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath == "" {
		t.Fatal("wallpaper not set")
	}
	if m.pregenFetches != 1 {
		t.Fatalf("pregenFetches=%d", m.pregenFetches)
	}
	if pool.Count(te.dir) != 1 {
		t.Fatalf("pool count=%d want 1", pool.Count(te.dir))
	}
	if pool.LoadPending(te.dir) != nil {
		t.Fatal("pending should be cleared after delivery")
	}

	// 第二轮: 池有图 → 换图走池(推进到 15:00 活跃时段); 池空后补池下新单
	te.now = te.now.Add(5 * time.Hour)
	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	if got := m.prompts[len(m.prompts)-1].Source; got != "pool" {
		t.Fatalf("second round source=%s want pool", got)
	}
	if m.pregenSubmits != 1 {
		t.Fatalf("pregenSubmits=%d want 1", m.pregenSubmits)
	}
	if pool.Count(te.dir) != 0 {
		t.Fatalf("pool should be consumed, count=%d", pool.Count(te.dir))
	}
}

func TestRunOnceLocalFallbackWhenLLMFails(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768), nextFail: true}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath == "" {
		t.Fatal("local fallback should still produce wallpaper")
	}
	if len(m.prompts) != 1 || m.prompts[0].Source != "local" {
		t.Fatalf("prompt report wrong: %+v", m.prompts)
	}
}

func TestRunOnceStyleShakeOnSatisfaction(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768)}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	te.askChoice = ChoiceStyle
	// 回访到期
	cfg, _ := config.Load(te.dir)
	cfg.Satisfaction.NextAskAt = te.now.Add(-time.Minute).Format(time.RFC3339)
	_ = cfg.Save(te.dir)

	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	if te.askCalls != 1 {
		t.Fatalf("askCalls=%d", te.askCalls)
	}
	// 换风格: 跳过预生成 + force_shake 传云端
	if m.pregenFetches != 0 {
		t.Fatalf("shake turn should skip pregen, fetches=%d", m.pregenFetches)
	}
	if !m.forceShake {
		t.Fatal("force_shake not sent to cloud")
	}
	// 强漂移事件上报
	if len(m.signalsGot) != 1 || len(m.signalsGot[0]) != 1 || m.signalsGot[0][0].Type != signals.TypeStyleReject {
		t.Fatalf("style_reject not reported: %+v", m.signalsGot)
	}
	// 周期重置为 3 天, 下次回访 = now+3d
	cfg2, _ := config.Load(te.dir)
	if cfg2.Satisfaction.IntervalDays != 3 {
		t.Fatalf("interval should reset to 3, got %d", cfg2.Satisfaction.IntervalDays)
	}
	want := te.now.AddDate(0, 0, 3).Format(time.RFC3339)
	if cfg2.Satisfaction.NextAskAt != want {
		t.Fatalf("next ask wrong: %s want %s", cfg2.Satisfaction.NextAskAt, want)
	}
	// 事件已清空(上报成功)
	pending, _ := signals.Load(te.dir)
	if len(pending) != 0 {
		t.Fatalf("signals should be cleared after report: %+v", pending)
	}
}

func TestRunOnceSatisfiedGrowsInterval(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768)}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	te.askChoice = ChoiceSatisfied
	cfg, _ := config.Load(te.dir)
	cfg.Satisfaction.NextAskAt = te.now.Add(-time.Minute).Format(time.RFC3339)
	_ = cfg.Save(te.dir)

	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	cfg2, _ := config.Load(te.dir)
	if cfg2.Satisfaction.IntervalDays != 7 {
		t.Fatalf("satisfied should grow interval to 7 (ladder), got %d", cfg2.Satisfaction.IntervalDays)
	}
	if len(m.signalsGot) != 1 || m.signalsGot[0][0].Type != signals.TypeStyleKeep {
		t.Fatalf("style_keep not reported: %+v", m.signalsGot)
	}
}

func TestRunOnceCloudDownKeepsWallpaper(t *testing.T) {
	// 指向关闭端口: 连接立即失败
	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, "http://127.0.0.1:1", te)
	if err := RunOnce(context.Background(), te.env("http://127.0.0.1:1", nil)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath != "" {
		t.Fatal("cloud down should keep old wallpaper")
	}
}

// TestRunOnceCloudDownUsesPool 云端不可达: 本地备用池兜底换图(断网可用核心场景)。
func TestRunOnceCloudDownUsesPool(t *testing.T) {
	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	setupClient(t, "http://127.0.0.1:1", te)
	if err := pool.Add(te.dir, pool.Entry{ProfileVersion: 1, Positive: "p", Seed: 1, CreatedAt: te.now.Format(time.RFC3339)}, gradientPNG(t, 1344, 768)); err != nil {
		t.Fatal(err)
	}
	if err := RunOnce(context.Background(), te.env("http://127.0.0.1:1", nil)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath == "" {
		t.Fatal("pool should be used when cloud is down")
	}
	if pool.Count(te.dir) != 0 {
		t.Fatalf("pool should be consumed, count=%d", pool.Count(te.dir))
	}
}

// TestChangeWindow 换图窗口判定: 开关/暂停/时段/锁屏/频率节流。
func TestChangeWindow(t *testing.T) {
	cfg := config.Default()
	env := Env{Quiet: func() bool { return false }}
	pol := policy.Default()
	at := func(h, m int) time.Time { return time.Date(2026, 9, 15, h, m, 0, 0, time.Local) }
	if ok, _ := changeWindow(env, cfg, pol, at(10, 0), false); !ok {
		t.Fatal("active window should allow change")
	}
	if ok, why := changeWindow(env, cfg, pol, at(13, 0), false); ok || why != "非活跃时段" {
		t.Fatalf("inactive got ok=%v why=%s", ok, why)
	}
	cfgOff := config.Default()
	cfgOff.AutoChange = false
	if ok, why := changeWindow(env, cfgOff, pol, at(10, 0), false); ok || why != "换图开关已关闭" {
		t.Fatalf("auto off got ok=%v why=%s", ok, why)
	}
	if ok, why := changeWindow(env, cfg, pol, at(10, 0), true); ok || why != "用户暂停中" {
		t.Fatalf("paused got ok=%v why=%s", ok, why)
	}
	quietEnv := Env{Quiet: func() bool { return true }}
	if ok, why := changeWindow(quietEnv, cfg, pol, at(10, 0), false); ok || why != "锁屏/全屏" {
		t.Fatalf("quiet got ok=%v why=%s", ok, why)
	}
	// 频率节流(2 小时频率: 1 小时差不够, 2 小时差放行)
	cfg3 := config.Default()
	cfg3.IntervalHours = 2
	cfg3.LastChangeAt = at(9, 0).Format(time.RFC3339)
	if ok, why := changeWindow(env, cfg3, pol, at(10, 0), false); ok || why != "换图频率未到" {
		t.Fatalf("throttle got ok=%v why=%s", ok, why)
	}
	if ok, _ := changeWindow(env, cfg3, pol, at(11, 0), false); !ok {
		t.Fatal("2h gap should pass throttle")
	}
}

func TestChooseGenSize(t *testing.T) {
	cases := []struct {
		sw, sh, w, h int
	}{
		{1920, 1080, 1920, 1080},
		{2560, 1440, 1920, 1080},
		{1024, 768, 1920, 1080},
		{3840, 2160, 1920, 1080},
	}
	for _, c := range cases {
		w, h := chooseGenSize(c.sw, c.sh)
		if w != c.w || h != c.h {
			t.Errorf("chooseGenSize(%d,%d)=%dx%d want %dx%d", c.sw, c.sh, w, h, c.w, c.h)
		}
	}
}
