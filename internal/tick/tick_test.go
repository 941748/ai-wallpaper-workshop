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
			writeEnv(w, map[string]any{"queued": true})
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
		Quiet:  func(*config.Config, time.Time) bool { return false },
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

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
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

func TestRunOncePregenHit(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768), pregenReady: true}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
	setupClient(t, m.srv.URL, te)
	if err := RunOnce(context.Background(), te.env(m.srv.URL, m)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath == "" {
		t.Fatal("wallpaper not set")
	}
	if m.nextCalls != 0 {
		t.Fatalf("pregen hit should skip LLM, nextCalls=%d", m.nextCalls)
	}
	if len(m.prompts) != 1 || m.prompts[0].Source != "pregen" {
		t.Fatalf("prompt report wrong: %+v", m.prompts)
	}
	if m.pregenFetches != 1 {
		t.Fatalf("pregenFetches=%d", m.pregenFetches)
	}
}

func TestRunOnceLocalFallbackWhenLLMFails(t *testing.T) {
	m := &mockCloud{pngData: gradientPNG(t, 1344, 768), nextFail: true}
	m.srv = httptest.NewServer(m.handler())
	defer m.srv.Close()

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
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

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
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

	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
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
	te := &testEnv{dir: t.TempDir(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)}
	setupClient(t, "http://127.0.0.1:1", te)
	if err := RunOnce(context.Background(), te.env("http://127.0.0.1:1", nil)); err != nil {
		t.Fatal(err)
	}
	if te.wpPath != "" {
		t.Fatal("cloud down should keep old wallpaper")
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
