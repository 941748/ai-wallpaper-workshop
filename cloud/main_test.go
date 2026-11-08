package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wallpaper/cloud/llm"
	"wallpaper/cloud/store"
	"wallpaper/internal/taxonomy"
)

// ---------- mock LLM ----------

func newMockLLM(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// 新架构: LLM 仅产出文案(positive/reason); 多余字段即使返回也会被忽略。
		out := map[string]any{
			"positive": "ink wash landscape, misty peaks at dawn, serene mood, wide composition, soft gradients",
			"reason":   "测试文案",
		}
		content, _ := json.Marshal(out)
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": string(content)}}},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	return httptest.NewServer(mux)
}

// ---------- 测试装配 ----------

type testServer struct {
	api  *API
	srv  *httptest.Server
	st   *store.Store
	data string
}

func newTestServer(t *testing.T, llmBase string, withLLM bool) *testServer {
	data := t.TempDir()
	for _, sub := range []string{"images", "pregen", "releases"} {
		if err := os.MkdirAll(data+"/"+sub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(data + "/cloud.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	llmClient := llm.New(llmBase, "", "mock")
	if withLLM {
		llmClient.Key = "test-key"
	}
	reg := DefaultRegistry()
	q := &queue{st: st, logf: func(string, ...any) {}}
	api := &API{st: st, llm: llmClient, reg: reg, q: q, dataDir: data,
		version: "test", logf: func(string, ...any) {},
		genHour: 12, genDay: 60, llmHour: 16, llmDay: 90, androidGenDay: 12}

	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	t.Cleanup(cancel)

	return &testServer{api: api, srv: httptest.NewServer(api.Routes()), st: st, data: data}
}

func (ts *testServer) register(t *testing.T) (string, string) {
	t.Helper()
	resp, err := http.Post(ts.srv.URL+"/api/v1/users/register", "application/json",
		strings.NewReader(`{"device":"test-pc"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			UserID string `json:"user_id"`
			Token  string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if !env.Success || env.Data.UserID == "" || env.Data.Token == "" {
		t.Fatalf("register failed: %+v", env)
	}
	return env.Data.UserID, env.Data.Token
}

func (ts *testServer) do(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func decodeData(t *testing.T, raw []byte, out any) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope parse: %v (%s)", err, raw)
	}
	if !env.Success {
		t.Fatalf("api error: %s", env.Error)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("data parse: %v (%s)", err, env.Data)
		}
	}
}

// ---------- 用例 ----------

// TestParsePolishTolerant LLM 文案解析: 兼容 markdown 包裹与多余字段; positive 缺失判无效。
func TestParsePolishTolerant(t *testing.T) {
	pos, reason, ok := parsePolish("```json\n{\"positive\":\"ink wash landscape, misty peaks\",\"junk\":123,\"reason\":\"测试\"}\n```")
	if !ok || pos == "" || reason != "测试" {
		t.Fatalf("polish parse failed: %q %q %v", pos, reason, ok)
	}
	if _, _, ok := parsePolish(`{"reason":"no positive"}`); ok {
		t.Fatal("missing positive must fail")
	}
	if _, _, ok := parsePolish("not json at all"); ok {
		t.Fatal("non-json must fail")
	}
}

func TestFullClientLifecycle(t *testing.T) {
	llmMock := newMockLLM(t)
	defer llmMock.Close()
	ts := newTestServer(t, llmMock.URL, true)
	defer ts.srv.Close()
	userID, token := ts.register(t)
	if !strings.HasPrefix(userID, "u_") {
		t.Fatalf("bad user id %s", userID)
	}

	// health
	resp, raw := ts.do(t, "GET", "/api/v1/health", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("health status %d", resp.StatusCode)
	}
	var health struct {
		OK      bool `json:"ok"`
		QuotaOK bool `json:"quota_ok"`
	}
	decodeData(t, raw, &health)
	if !health.OK || !health.QuotaOK {
		t.Fatalf("health = %+v", health)
	}

	// prompts/next: 组合由本地引擎按画像确定(ink), LLM 仅润色文案; 尺寸/工作流由本地决定
	resp, raw = ts.do(t, "POST", "/api/v1/prompts/next", token, map[string]any{
		"profile_version": 1,
		"profile":         map[string]float64{"style/ink": 0.9},
		"screen_w":        1920, "screen_h": 1080,
		"force_shake": false,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("next status %d: %s", resp.StatusCode, raw)
	}
	var nresp struct {
		Positive   string            `json:"positive"`
		Width      int               `json:"width"`
		Height     int               `json:"height"`
		WorkflowID string            `json:"workflow_id"`
		Combo      map[string]string `json:"combo"`
		Source     string            `json:"source"`
		Seed       int64             `json:"seed"`
	}
	decodeData(t, raw, &nresp)
	if nresp.Source != "llm" {
		t.Fatalf("source = %s", nresp.Source)
	}
	if nresp.WorkflowID != "wf-base" {
		t.Fatalf("workflow should be local default, got %s", nresp.WorkflowID)
	}
	if nresp.Width%8 != 0 || nresp.Height%8 != 0 {
		t.Fatalf("size not multiple of 8: %dx%d", nresp.Width, nresp.Height)
	}
	// 组合必须严格跟随画像: style/ink=0.9 是唯一高权值, 采样必须命中 ink, 不得被 LLM 改写
	if nresp.Combo["style"] != "ink" {
		t.Fatalf("combo style must follow profile, got %v", nresp.Combo)
	}
	if nresp.Seed == 0 {
		t.Fatal("seed should be set by local engine")
	}
	if !strings.Contains(nresp.Positive, "ink wash landscape") {
		t.Fatalf("positive should be LLM polishing output: %s", nresp.Positive)
	}

	// signals 上报 + 漂移摘要
	resp, raw = ts.do(t, "POST", "/api/v1/signals", token, map[string]any{
		"events": []map[string]any{{
			"id": "ev1", "at": time.Now(), "type": "style_reject", "detail": "换个风格",
		}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("signals status %d: %s", resp.StatusCode, raw)
	}
	resp, raw = ts.do(t, "GET", "/api/v1/profile/drift", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("drift status %d", resp.StatusCode)
	}
	var drift struct {
		Summary string `json:"summary"`
	}
	decodeData(t, raw, &drift)
	if !strings.Contains(drift.Summary, "换风格") {
		t.Fatalf("drift summary missing: %s", drift.Summary)
	}

	// generate: 提交 → 轮询 → 下载图片
	resp, raw = ts.do(t, "POST", "/api/v1/generate", token, map[string]any{
		"positive": "test prompt", "negative": "",
		"width": 1920, "height": 1080, "seed": 1,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("generate status %d: %s", resp.StatusCode, raw)
	}
	var genResp struct {
		JobID string `json:"job_id"`
	}
	decodeData(t, raw, &genResp)
	// S3: 云端不再直连渲染, 测试内联"代理完成"(领任务→本地落盘 JPEG→置 done)
	completeClaimed := func() {
		t.Helper()
		job, err := ts.st.ClaimNextJob()
		if err != nil || job == nil {
			t.Fatal("应有可领任务")
		}
		dir := "images"
		if job.Kind == "pregen" {
			dir = "pregen"
		}
		path := filepath.Join(ts.data, dir, job.JobID+".jpg")
		if err := os.WriteFile(path, testJPEG(), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = ts.st.SetJobStatus(job.JobID, "done", "", path)
		if job.Kind == "pregen" {
			_ = ts.st.SetPregenReady(job.JobID, path)
		} else {
			_ = ts.st.ArchiveJob(job.JobID)
		}
	}
	completeClaimed()
	status, jobErr := waitJob(t, ts, token, genResp.JobID, 10*time.Second)
	if status != "done" {
		t.Fatalf("job status = %s (err=%s)", status, jobErr)
	}
	resp, imgRaw := ts.do(t, "GET", "/api/v1/generate/"+genResp.JobID+"/image", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("image status %d", resp.StatusCode)
	}
	if _, format, err := image.Decode(bytes.NewReader(imgRaw)); err != nil || format != "jpeg" {
		t.Fatalf("image not jpeg (format=%q): %v", format, err)
	}

	// 越权访问他人任务
	_, otherToken := ts.register(t)
	resp, _ = ts.do(t, "GET", "/api/v1/generate/"+genResp.JobID+"", otherToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-user job access should be 403, got %d", resp.StatusCode)
	}

	// pregen: 下单 → 就绪 → 取图
	resp, raw = ts.do(t, "POST", "/api/v1/pregen", token, map[string]any{
		"profile_version": 2, "profile": map[string]float64{}, "screen_w": 1920, "screen_h": 1080,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("pregen submit %d: %s", resp.StatusCode, raw)
	}
	completeClaimed() // 模拟代理把预生成渲染完成
	// 30s 上限: 成功路径 ready 即 break 不变慢; CI(windows-latest) 慢机器上
	// 队列 worker 调度可达 10s+, 原 10s 会 flaky 超时(见 v1.0.5 首发 CI 失败)。
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		_, praw := ts.do(t, "GET", "/api/v1/pregen?profile_version=2", token, nil)
		var pr struct {
			Ready bool `json:"ready"`
		}
		decodeData(t, praw, &pr)
		if pr.Ready {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("pregen not ready in time")
	}
	resp, pimg := ts.do(t, "GET", "/api/v1/pregen/image", token, nil)
	if resp.StatusCode != 200 || len(pimg) == 0 {
		t.Fatalf("pregen image status %d len %d", resp.StatusCode, len(pimg))
	}
	// 画像版本不匹配 → 不返回
	_, praw := ts.do(t, "GET", "/api/v1/pregen?profile_version=99", token, nil)
	var pr99 struct {
		Ready bool `json:"ready"`
	}
	decodeData(t, praw, &pr99)
	if pr99.Ready {
		t.Fatal("pregen with wrong profile_version must not be ready")
	}

	// 设备配对: create → redeem
	resp, raw = ts.do(t, "POST", "/api/v1/devices/link", token, map[string]any{"action": "create", "device": "old-pc"})
	if resp.StatusCode != 200 {
		t.Fatalf("link create %d: %s", resp.StatusCode, raw)
	}
	var link struct {
		Code string `json:"code"`
	}
	decodeData(t, raw, &link)
	if len(link.Code) != 6 {
		t.Fatalf("bad code %s", link.Code)
	}
	_, raw = ts.do(t, "POST", "/api/v1/devices/link", "", map[string]any{"action": "redeem", "code": link.Code, "device": "new-pc"})
	var redeemed struct {
		UserID string `json:"user_id"`
		Token  string `json:"token"`
	}
	decodeData(t, raw, &redeemed)
	if redeemed.UserID != userID || redeemed.Token != token {
		t.Fatalf("redeem mismatch: %+v", redeemed)
	}
	// 已使用的码不可再用
	resp, _ = ts.do(t, "POST", "/api/v1/devices/link", "", map[string]any{"action": "redeem", "code": link.Code})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("used code should 404, got %d", resp.StatusCode)
	}
}

func waitJob(t *testing.T, ts *testServer, token, jobID string, timeout time.Duration) (string, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, raw := ts.do(t, "GET", "/api/v1/generate/"+jobID, token, nil)
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		decodeData(t, raw, &st)
		if st.Status == "done" || st.Status == "failed" {
			return st.Status, st.Error
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "timeout", ""
}

func TestLLMDisabledFallbackLocal(t *testing.T) {
	ts := newTestServer(t, "", false)
	defer ts.srv.Close()
	_, token := ts.register(t)
	_, raw := ts.do(t, "POST", "/api/v1/prompts/next", token, map[string]any{
		"profile_version": 1, "screen_w": 1920, "screen_h": 1080,
	})
	var nresp struct {
		Source string `json:"source"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	decodeData(t, raw, &nresp)
	if nresp.Source != "local" {
		t.Fatalf("without LLM key source should be local, got %s", nresp.Source)
	}
	if nresp.Width != 1920 || nresp.Height != 1080 {
		t.Fatalf("bucket size wrong: %dx%d", nresp.Width, nresp.Height)
	}
}

func TestQuotaExceeded(t *testing.T) {
	ts := newTestServer(t, "", false)
	defer ts.srv.Close()
	ts.api.llmHour = 1
	_, token := ts.register(t)
	// 第一次占用 llm 配额
	resp, _ := ts.do(t, "POST", "/api/v1/prompts/next", token, map[string]any{"profile_version": 1})
	if resp.StatusCode != 200 {
		t.Fatalf("first call should pass, got %d", resp.StatusCode)
	}
	// 第二次超限
	resp, raw := ts.do(t, "POST", "/api/v1/prompts/next", token, map[string]any{"profile_version": 1})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d: %s", resp.StatusCode, raw)
	}
	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Error != "quota_exceeded" {
		t.Fatalf("error = %s", env.Error)
	}
}

// TestNextQuotaPrecheck 出图配额耗尽时 /prompts/next 在 LLM 规划前秒回429(不白烧规划次数);
// 安卓设备的 LLM 计数进独立作用域(uid@android), PC 池不受影响。
func TestNextQuotaPrecheck(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)
	if err := ts.st.UpsertDeviceAttrs(store.DeviceAttr{Device: "and-p", UserID: uid, Type: "android"}); err != nil {
		t.Fatal(err)
	}
	u, err := ts.st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatal("用户缺失")
	}

	// 安卓 next 成功: LLM 计数进 uid@android, 不进 PC 池
	resp, raw := ts.do(t, "POST", "/api/v1/prompts/next", tok, map[string]any{
		"profile_version": 1, "device_id": "and-p",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首次应200: %d %s", resp.StatusCode, raw)
	}
	if llmA, _, _ := ts.st.UsageHour(uid + "@android"); llmA != 1 {
		t.Fatalf("安卓 llm 应=1, got %d", llmA)
	}
	if llmP, _, _ := ts.st.UsageHour(uid); llmP != 0 {
		t.Fatalf("PC llm 池不应被安卓挤占, got %d", llmP)
	}

	// 耗尽安卓出图日配额 → next 秒回429, 且不再消耗 LLM 规划次数
	for i := 0; i < ts.api.androidGenDay; i++ {
		if err := ts.st.BumpUsage(uid+"@android", "gen"); err != nil {
			t.Fatal(err)
		}
	}
	resp, raw = ts.do(t, "POST", "/api/v1/prompts/next", tok, map[string]any{
		"profile_version": 1, "device_id": "and-p",
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("配额耗尽应429: %d %s", resp.StatusCode, raw)
	}
	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Error != "quota_exceeded" {
		t.Fatalf("error = %s", env.Error)
	}
	if llmA, _, _ := ts.st.UsageHour(uid + "@android"); llmA != 1 {
		t.Fatalf("秒回应不再消耗 llm, got %d", llmA)
	}

	// PC 作用域不受影响
	resp, raw = ts.do(t, "POST", "/api/v1/prompts/next", tok, map[string]any{"profile_version": 1})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PC next 应200: %d %s", resp.StatusCode, raw)
	}
}

// ---------- 评价信号描述 ----------

func TestSignalDescWithCombo(t *testing.T) {
	got := signalDesc("liked", map[string]any{"combo": map[string]any{"style": "ink"}})
	if !strings.Contains(got, "喜欢") || !strings.Contains(got, "(") {
		t.Fatalf("liked 事件应附带组合描述: %s", got)
	}
	got = signalDesc("disliked", map[string]any{"combo": map[string]any{"style": "ink"}})
	if !strings.Contains(got, "不喜欢") {
		t.Fatalf("disliked 事件描述错误: %s", got)
	}
	if signalDesc("reconfigure", nil) != "reconfigure" {
		t.Fatal("非评价事件应原样返回")
	}
	if signalDesc("disliked", nil) != "disliked" {
		t.Fatal("无 combo 的评价事件应退回类型名")
	}
}

// ---------- 语境引擎 ----------

func TestContextHints(t *testing.T) {
	now := time.Date(2026, 2, 16, 10, 0, 0, 0, time.Local)
	hints := contextHints(now, false)
	if !strings.Contains(strings.Join(hints, ";"), "春节") {
		t.Fatalf("应命中春节: %v", hints)
	}
	if contextHints(now, true) != nil {
		t.Fatal("关闭开关时不应返回语境")
	}
	// 远离节日的窗口应为空
	far := time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local)
	if len(contextHints(far, false)) != 0 {
		t.Fatalf("7 月初不应有语境: %v", contextHints(far, false))
	}
}

// ---------- 存量任务清理 ----------

// TestPurgeJobsByKind 探针机制退役: 启动清理应删掉存量 probe 任务(含 queued), 不影响其他类型。
func TestPurgeJobsByKind(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(data + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateJob(store.Job{JobID: "probe-p01", UserID: "system", Kind: "probe",
		Priority: "idle", Positive: "x", Width: 1024, Height: 768, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateJob(store.Job{JobID: "gen-keep", UserID: "u", Kind: "gen",
		Priority: "normal", Positive: "x", Width: 1024, Height: 768, Seed: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeJobsByKind("probe"); err != nil {
		t.Fatal(err)
	}
	if j, _ := st.Job("probe-p01"); j != nil {
		t.Fatalf("probe 任务应被清理, got %+v", j)
	}
	if j, _ := st.Job("gen-keep"); j == nil {
		t.Fatal("其他类型任务不应被清理")
	}
}

// ---------- 客户端自更新 ----------

func TestClientLatestURLOverride(t *testing.T) {
	ts := newTestServer(t, "", false)
	manifestPath := ts.data + "/releases/manifest.json"

	// 含 url 覆盖(Gitee 直链): 应原样返回
	override := "https://gitee.com/x/y/releases/download/v1.0.2/AIWallpaper.exe"
	body := `{"version":"1.0.2","file":"AIWallpaper-1.0.2.exe","sha256":"aa","size":7,"url":"` + override + `"}`
	if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := latestData(t, ts, "1.0.1")
	if got.URL != override {
		t.Fatalf("url 覆盖未生效: got %q, want %q", got.URL, override)
	}
	if got.Version != "1.0.2" || got.Size != 7 {
		t.Fatalf("元数据异常: %+v", got)
	}

	// 缺省(无 url 字段): 回退为本站 /releases/ 拼装
	body = `{"version":"1.0.2","file":"AIWallpaper-1.0.2.exe","sha256":"aa","size":7}`
	if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got = latestData(t, ts, "1.0.1")
	if !strings.HasSuffix(got.URL, "/releases/AIWallpaper-1.0.2.exe") || strings.Contains(got.URL, "gitee.com") {
		t.Fatalf("缺省下载地址异常: %q", got.URL)
	}

	// 同版本: 无更新(不应下发下载地址)
	got = latestData(t, ts, "1.0.2")
	if got.URL != "" || got.Version != "1.0.2" {
		t.Fatalf("同版本不应返回下载地址: %+v", got)
	}
}

// TestClientVersionRecorded 更新检查带鉴权时应把客户端版本落库(看板版本分布数据源); 无鉴权不记录。
func TestClientVersionRecorded(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)

	resp, _ := ts.do(t, "GET", "/api/v1/client/latest?version=9.9.7", tok, nil)
	_ = resp.Body.Close()

	rows, err := ts.st.AdminUserPrefRows()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.UserID == uid {
			found = true
			if r.ClientVer != "9.9.7" {
				t.Fatalf("版本未落库: got %q, want 9.9.7", r.ClientVer)
			}
		}
	}
	if !found {
		t.Fatal("注册用户未出现在看板用户行")
	}

	// 无鉴权的更新检查不应写入(也绝不 panic)
	resp2, _ := ts.do(t, "GET", "/api/v1/client/latest?version=8.8.8", "", nil)
	_ = resp2.Body.Close()
	rows, err = ts.st.AdminUserPrefRows()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.UserID == uid && r.ClientVer != "9.9.7" {
			t.Fatalf("无鉴权请求覆盖了版本: got %q", r.ClientVer)
		}
	}
}

type latestResp struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Notes   string `json:"notes"`
}

// latestData 请求 /api/v1/client/latest 并解包 data 字段。
func latestData(t *testing.T, ts *testServer, version string) latestResp {
	t.Helper()
	resp, err := http.Get(ts.srv.URL + "/api/v1/client/latest?version=" + version)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Success bool       `json:"success"`
		Data    latestResp `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Success {
		t.Fatalf("接口未成功: %+v", out)
	}
	return out.Data
}

// ---------- 运营策略 ----------

func TestPolicyEndpoint(t *testing.T) {
	ts := newTestServer(t, "", false)
	// 未部署 policy.json: 返回内置默认且匿名可拉
	resp, raw := ts.do(t, "GET", "/api/v1/policy", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("policy status %d: %s", resp.StatusCode, raw)
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Version      int         `json:"policy_version"`
			ActiveBlocks [][2]string `json:"active_blocks"`
			PoolTarget   int         `json:"pool_target"`
			PauseOptions []int       `json:"pause_options_hours"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		t.Fatalf("policy decode: %v %s", err, raw)
	}
	if env.Data.PoolTarget != 3 || len(env.Data.ActiveBlocks) != 3 || len(env.Data.PauseOptions) != 3 {
		t.Fatalf("default policy unexpected: %+v", env.Data)
	}
	// 部署 policy.json: 改文件即生效(无需重启)
	custom := `{"policy_version":5,"active_blocks":[["08:00","10:00"]],"pool_target":4,` +
		`"pause_options_hours":[2,12],"max_pause_hours":12}`
	if err := os.WriteFile(ts.data+"/policy.json", []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	_, raw = ts.do(t, "GET", "/api/v1/policy", "", nil)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Version != 5 || env.Data.PoolTarget != 4 || len(env.Data.PauseOptions) != 2 {
		t.Fatalf("custom policy not applied: %+v", env.Data)
	}
}

// ---------- P1-a 安卓配套服务端基座 ----------

// TestProfileDownlink 画像下行: 字段齐全、packs 三态透传、screen 不下发(方案A)。
func TestProfileDownlink(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)
	if err := ts.st.SaveProfileSnapshot(store.ProfileSnapshot{
		UserID: uid, ProfileVersion: 5,
		Profile:  `{"style/realism":0.9}`,
		Disliked: `["subject/weapons"]`,
		Custom:   `["subject/cat"]`,
		Packs:    `[]`,
		ScreenW:  2240, ScreenH: 1400,
	}); err != nil {
		t.Fatal(err)
	}
	_, raw := ts.do(t, "GET", "/api/v1/profile", tok, nil)
	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		t.Fatalf("响应异常: %s", raw)
	}
	d := env.Data
	if int(d["profile_version"].(float64)) != 5 {
		t.Fatalf("profile_version: %v", d["profile_version"])
	}
	if d["profile"].(map[string]any)["style/realism"].(float64) != 0.9 {
		t.Fatalf("profile 内容: %v", d["profile"])
	}
	if len(d["disliked"].([]any)) != 1 || len(d["custom"].([]any)) != 1 {
		t.Fatalf("disliked/custom: %v %v", d["disliked"], d["custom"])
	}
	if d["packs"] == nil {
		t.Fatal("显式清空的 packs 应为 [] 而非 null")
	}
	if len(d["packs"].([]any)) != 0 {
		t.Fatalf("packs 应为空数组: %v", d["packs"])
	}
	if _, has := d["screen_w"]; has {
		t.Fatal("screen 属设备维度, 不应下发")
	}

	// packs 未设置(旧档): 应为 null(三态的"推断"语义)
	if err := ts.st.SaveProfileSnapshot(store.ProfileSnapshot{UserID: uid, ProfileVersion: 6}); err != nil {
		t.Fatal(err)
	}
	_, raw = ts.do(t, "GET", "/api/v1/profile", tok, nil)
	_ = json.Unmarshal(raw, &env)
	if env.Data["packs"] != nil {
		t.Fatalf("未设置 packs 应为 null: %v", env.Data["packs"])
	}
}

// TestDeviceInfoRegister device_info 上报应落 devices 新列(安卓属性)。
func TestDeviceInfoRegister(t *testing.T) {
	ts := newTestServer(t, "", false)
	resp, raw := ts.do(t, "POST", "/api/v1/users/register", "", map[string]any{
		"device": "and-1",
		"device_info": map[string]any{
			"type": "android", "model": "Xiaomi 14", "os": "Android", "os_version": "14",
			"app_version": "1.0.0", "screen_w": 1080, "screen_h": 2400,
		},
	})
	_ = resp.Body.Close()
	var env struct {
		Data struct {
			UserID string `json:"user_id"`
			Token  string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Data.UserID == "" {
		t.Fatalf("注册失败: %s", raw)
	}
	rows, err := ts.st.AdminDeviceRows(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Device == "and-1" {
			found = true
			if r.Type != "android" || r.Model != "Xiaomi 14" || r.OS != "Android" ||
				r.OSVersion != "14" || r.AppVer != "1.0.0" || r.ScreenW != 1080 || r.ScreenH != 2400 {
				t.Fatalf("device_info 未落库: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("设备未登记")
	}
}

// TestAndroidSnapshotGuard 安卓渲染带 device_id: 屏幕进 devices, 不回写共享快照 screen(方案A)。
func TestAndroidSnapshotGuard(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, _ := ts.register(t)
	// PC 先绘(无 device_id): 快照 screen=PC 主屏
	ts.api.saveProfileSnapshot(uid, nextReq{
		ProfileVersion: 1, ScreenW: 2240, ScreenH: 1400,
		Profile: map[string]float64{"style/realism": 0.9},
	})
	// 安卓设备登记(带 screen)
	if err := ts.st.UpsertDeviceAttrs(store.DeviceAttr{
		Device: "and-1", UserID: uid, Type: "android",
		Model: "Xiaomi 14", ScreenW: 1080, ScreenH: 2400,
	}); err != nil {
		t.Fatal(err)
	}
	// 安卓渲染(带 device_id): 快照 screen 应保持 PC 值, 设备行更新
	ts.api.saveProfileSnapshot(uid, nextReq{
		ProfileVersion: 2, ScreenW: 1080, ScreenH: 2400, DeviceID: "and-1",
		Profile: map[string]float64{"style/realism": 0.9},
	})
	snap, err := ts.st.LoadProfileSnapshot(uid)
	if err != nil || snap == nil {
		t.Fatal("快照缺失")
	}
	if snap.ScreenW != 2240 || snap.ScreenH != 1400 {
		t.Fatalf("共享快照 screen 被安卓污染: %dx%d", snap.ScreenW, snap.ScreenH)
	}
	if snap.ProfileVersion != 2 {
		t.Fatalf("画像版本应随渲染更新: %d", snap.ProfileVersion)
	}
	rows, _ := ts.st.AdminDeviceRows(10)
	for _, r := range rows {
		if r.Device == "and-1" && (r.ScreenW != 1080 || r.ScreenH != 2400) {
			t.Fatalf("设备屏幕未更新: %+v", r)
		}
	}
	// RecentPCScreen: 安卓不算 PC
	if w, h, _ := ts.st.RecentPCScreen(uid); w != 0 || h != 0 {
		t.Fatalf("纯安卓不应返回 PC 屏幕: %dx%d", w, h)
	}
	// PC 设备登记后可取
	if err := ts.st.UpsertDeviceAttrs(store.DeviceAttr{
		Device: "pc-1", UserID: uid, Type: "pc", ScreenW: 2240, ScreenH: 1400,
	}); err != nil {
		t.Fatal(err)
	}
	if w, h, _ := ts.st.RecentPCScreen(uid); w != 2240 || h != 1400 {
		t.Fatalf("RecentPCScreen: %dx%d", w, h)
	}
}

// TestAndroidLatestAndGoAPK 安卓检查更新 + /go/apk 计数 302 中转。
func TestAndroidLatestAndGoAPK(t *testing.T) {
	ts := newTestServer(t, "", false)

	// 未发布: 返回同版本(无更新)
	_, raw := ts.do(t, "GET", "/api/v1/android/latest?version=1.0.0", "", nil)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.0.0" || env.Data["url"] != nil {
		t.Fatalf("未发布语义: %v", env.Data)
	}

	body := `{"version":"1.0.1","file":"AIWallpaper-Android-1.0.1.apk","sha256":"ab","size":9,` +
		`"notes":"n","url":"https://www.cybermesh.space/go/apk"}`
	if err := os.WriteFile(ts.data+"/releases/manifest-android.json", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, raw = ts.do(t, "GET", "/api/v1/android/latest?version=1.0.0", "", nil)
	env.Data = nil // 重置 map: json.Unmarshal 不会清除上一轮残留 key
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.0.1" || env.Data["url"] != "https://www.cybermesh.space/go/apk" {
		t.Fatalf("更新下发: %v", env.Data)
	}
	_, raw = ts.do(t, "GET", "/api/v1/android/latest?version=1.0.1", "", nil)
	env.Data = nil
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.0.1" || env.Data["url"] != nil {
		t.Fatalf("同版本不应有更新: %v", env.Data)
	}

	// /go/apk: 302 到 Gitee 资产约定 URL + 记 apk_dl(不跟随跳转)
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequest(http.MethodGet, ts.srv.URL+"/go/apk", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("状态码: %d", resp.StatusCode)
	}
	want := "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/android-v1.0.1/AIWallpaper-Android-1.0.1.apk"
	if loc := resp.Header.Get("Location"); loc != want {
		t.Fatalf("Location: got %q want %q", loc, want)
	}
	if n, _ := ts.st.SiteEventCount("apk_dl"); n != 1 {
		t.Fatalf("apk_dl 计数: %d", n)
	}

	// 未发布 manifest: 404
	if err := os.Remove(ts.data + "/releases/manifest-android.json"); err != nil {
		t.Fatal(err)
	}
	resp2, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("无 manifest 应 404: %d", resp2.StatusCode)
	}
}

// TestAndroidQuotaScope 安卓出图配额独立计数(uid@android 作用域), PC 共享池不受影响;
// profile.paired 按是否存在 PC 设备判定(安卓据此隐藏配对框)。
func TestAndroidQuotaScope(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)
	if err := ts.st.UpsertDeviceAttrs(store.DeviceAttr{Device: "and-q", UserID: uid, Type: "android"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.UpsertDeviceAttrs(store.DeviceAttr{Device: "pc-q", UserID: uid, Type: "pc"}); err != nil {
		t.Fatal(err)
	}
	u, err := ts.st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatal("用户缺失")
	}

	// 作用域判定
	if got := ts.api.genScope(u, "and-q"); got != uid+"@android" {
		t.Fatalf("安卓 scope: %q", got)
	}
	if got := ts.api.genScope(u, "pc-q"); got != uid {
		t.Fatalf("PC scope 应共享: %q", got)
	}
	if got := ts.api.genScope(u, ""); got != uid {
		t.Fatalf("无设备应共享: %q", got)
	}

	// 用尽安卓日配额(12), PC 共享池应不受影响
	for i := 0; i < 12; i++ {
		if err := ts.st.BumpUsage(uid+"@android", "gen"); err != nil {
			t.Fatal(err)
		}
	}
	if ts.api.checkGenQuota(u, "and-q") {
		t.Fatal("安卓配额应已用尽(12/天)")
	}
	if !ts.api.checkGenQuota(u, "pc-q") {
		t.Fatal("PC 共享池不应被安卓用量挤占")
	}
	if !ts.api.checkGenQuota(u, "") {
		t.Fatal("无设备作用域不应受影响")
	}

	// profile.paired: 有 PC 设备 → true
	_, raw := ts.do(t, "GET", "/api/v1/profile", tok, nil)
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["paired"] != true {
		t.Fatalf("paired 应为 true: %v", env.Data["paired"])
	}

	// 纯安卓用户(无 PC 设备) → false
	resp, raw2 := ts.do(t, "POST", "/api/v1/users/register", "", map[string]any{
		"device": "and-only", "device_info": map[string]any{"type": "android"},
	})
	_ = resp.Body.Close()
	var reg struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw2, &reg); err != nil || reg.Data.Token == "" {
		t.Fatalf("注册失败: %s", raw2)
	}
	_, raw3 := ts.do(t, "GET", "/api/v1/profile", reg.Data.Token, nil)
	env.Data = nil
	if err := json.Unmarshal(raw3, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["paired"] != false {
		t.Fatalf("纯安卓用户 paired 应为 false: %v", env.Data["paired"])
	}
}

// TestProfilePushAndGuard 画像编辑推送(安卓点选→双端同步): pv 服务端授版、
// 落后版本的出图上传被守卫拦住不覆盖、校验≥1包或≥1喜欢、labels/dict 下发。
func TestProfilePushAndGuard(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)

	// 1) 校验: 空画像+空包 → 400
	resp, raw := ts.do(t, "POST", "/api/v1/profile", tok, map[string]any{
		"profile": map[string]float64{},
		"packs":   []string{},
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空画像应 400, got %d (%s)", resp.StatusCode, raw)
	}

	// 2) 正常推送: 一个喜欢 + 一个包
	resp, raw = ts.do(t, "POST", "/api/v1/profile", tok, map[string]any{
		"profile": map[string]float64{"style/realism": 0.9},
		"packs":   []string{"写实美女"},
	})
	_ = resp.Body.Close()
	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		t.Fatalf("推送失败: %s", raw)
	}
	if int(env.Data["profile_version"].(float64)) != 1 {
		t.Fatalf("首推 pv 应为1: %v", env.Data)
	}

	// 3) GET 应回带 labels/dict/packs
	_, raw = ts.do(t, "GET", "/api/v1/profile", tok, nil)
	env.Data = nil
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["profile_version"].(float64) != 1 {
		t.Fatalf("GET pv: %v", env.Data["profile_version"])
	}
	labels := env.Data["labels"].(map[string]any)
	if labels["style/realism"] != "写实" {
		t.Fatalf("labels 应含中文: %v", labels)
	}
	dict := env.Data["dict"].(map[string]any)
	dims := dict["dimensions"].([]any)
	if len(dims) != len(taxonomy.Dimensions) {
		t.Fatalf("dict 维度数: %d", len(dims))
	}
	dpacks := dict["packs"].([]any)
	if len(dpacks) != len(taxonomy.PresetPacks) {
		t.Fatalf("dict 包数: %d", len(dpacks))
	}
	firstPack := dpacks[0].(map[string]any)
	if len(firstPack["covers"].([]any)) == 0 {
		t.Fatal("包 covers 不应为空")
	}

	// 4) 落后版本的出图上传(如 PC 旧本地画像) → 守卫: 画像保持推送值
	ts.api.saveProfileSnapshot(uid, nextReq{
		ProfileVersion: 0, // 落后于 pv=1
		Profile:        map[string]float64{"subject/weapons": 0.9},
		ScreenW:        2240, ScreenH: 1400,
	})
	snap, err := ts.st.LoadProfileSnapshot(uid)
	if err != nil || snap == nil {
		t.Fatal("快照缺失")
	}
	if snap.ProfileVersion != 1 {
		t.Fatalf("守卫应保持 pv=1, got %d", snap.ProfileVersion)
	}
	var pm map[string]float64
	_ = json.Unmarshal([]byte(snap.Profile), &pm)
	if _, hit := pm["subject/weapons"]; hit {
		t.Fatal("旧画像不应覆盖新编辑")
	}
	if _, ok := pm["style/realism"]; !ok {
		t.Fatal("推送的画像应保留")
	}
	// screen 仍随上传更新(设备维度不受画像守卫影响)
	if snap.ScreenW != 2240 || snap.ScreenH != 1400 {
		t.Fatalf("screen 应随请求更新: %dx%d", snap.ScreenW, snap.ScreenH)
	}

	// 5) 二次推送: pv 递增
	resp, raw = ts.do(t, "POST", "/api/v1/profile", tok, map[string]any{
		"profile": map[string]float64{"style/realism": 0.9, "subject/cat": 0.9},
		"packs":   []string{"治愈日常"},
	})
	_ = resp.Body.Close()
	env.Data = nil
	_ = json.Unmarshal(raw, &env)
	if int(env.Data["profile_version"].(float64)) != 2 {
		t.Fatalf("二推 pv 应为2: %v", env.Data)
	}
}

// TestAgentRenderFlow S3 全链路: 领任务(正反提示词/尺寸/seed/OSS键下发, 置 running) →
// 回报完成(done+oss_key 服务端核对+归档+pregen 联动, 幂等) → 失败回报 → 卡死回收 → health。
func TestAgentRenderFlow(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, _ := ts.register(t)
	t.Setenv("AW_AGENT_TOKEN", "test-agent-token")

	agentPost := func(path string, payload any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer test-agent-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, readAll(resp)
	}
	claim := func() (int, []byte, map[string]any) {
		t.Helper()
		code, raw := agentPost("/api/v1/agent/claim", map[string]any{"version": "9.9.9"})
		var env struct {
			Data struct {
				Task map[string]any `json:"task"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &env)
		return code, raw, env.Data.Task
	}

	// 1) 鉴权: 错 token401
	req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+"/api/v1/agent/claim", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错 token 应401, got %d", resp.StatusCode)
	}

	// 2) 领任务: 全字段下发 + 置 running
	if err := ts.st.CreateJob(store.Job{
		JobID: "j-agent1", UserID: uid, Kind: "gen", Priority: "normal",
		Positive: "阳光海岸", Negative: "低质量, 模糊", Width: 1344, Height: 768,
		Seed: 42, WorkflowID: "std",
	}); err != nil {
		t.Fatal(err)
	}
	code, raw, task := claim()
	if code != 200 {
		t.Fatalf("claim: %d %s", code, raw)
	}
	if task == nil || task["job_id"] != "j-agent1" || task["positive"] != "阳光海岸" ||
		task["negative"] != "低质量, 模糊" || task["width"] != float64(1344) ||
		task["seed"] != float64(42) || task["workflow_id"] != "std" ||
		task["oss_key"] != "u/"+uid+"/j-agent1.jpg" {
		t.Fatalf("claim 任务字段: %+v", task)
	}
	if j, _ := ts.st.Job("j-agent1"); j == nil || j.Status != "running" {
		t.Fatalf("领走应置 running: %+v", j)
	}

	// 3) 空队列 → task=null
	code, raw, task = claim()
	if code != 200 || task != nil {
		t.Fatalf("空队列应 task=null: %d %s", code, raw)
	}

	// 4) 回报成功 → done + oss_key + 归档; 幂等
	code, raw = agentPost("/api/v1/agent/rendered", map[string]any{
		"job_id": "j-agent1", "ok": true, "oss_key": "u/" + uid + "/j-agent1.jpg",
	})
	if code != 200 {
		t.Fatalf("rendered: %d %s", code, raw)
	}
	j, _ := ts.st.Job("j-agent1")
	if j == nil || j.Status != "done" || j.OssKey != "u/"+uid+"/j-agent1.jpg" {
		t.Fatalf("done 状态: %+v", j)
	}
	if arc, _ := ts.st.Archive("j-agent1"); arc == nil {
		t.Fatal("gen 完成应落归档")
	}
	if code, _ := agentPost("/api/v1/agent/rendered", map[string]any{"job_id": "j-agent1", "ok": true}); code != 200 {
		t.Fatalf("幂等回报应200, got %d", code)
	}

	// 5) oss_key 不匹配 → 409 且不落 done
	if err := ts.st.CreateJob(store.Job{JobID: "j-agent2", UserID: uid, Kind: "gen", Priority: "normal", Positive: "p"}); err != nil {
		t.Fatal(err)
	}
	_, _, _ = claim() // 领走 j-agent2(队列里只剩它)
	code, raw = agentPost("/api/v1/agent/rendered", map[string]any{"job_id": "j-agent2", "ok": true, "oss_key": "u/other/j.jpg"})
	if code != http.StatusConflict {
		t.Fatalf("键不匹配应409: %d %s", code, raw)
	}
	if j, _ := ts.st.Job("j-agent2"); j == nil || j.Status != "running" {
		t.Fatalf("409 后不应落 done: %+v", j)
	}

	// 6) 失败回报 → failed + 错误信息
	code, raw = agentPost("/api/v1/agent/rendered", map[string]any{"job_id": "j-agent2", "ok": false, "error": "ComfyUI 崩了"})
	if code != 200 {
		t.Fatalf("失败回报应200: %d %s", code, raw)
	}
	if j, _ := ts.st.Job("j-agent2"); j == nil || j.Status != "failed" || j.Error != "ComfyUI 崩了" {
		t.Fatalf("failed 状态: %+v", j)
	}

	// 7) pregen 联动: 领走 → 回报 → ready + oss_key
	if err := ts.st.CreateJob(store.Job{JobID: "pg-agent1", UserID: uid, Kind: "pregen", Priority: "idle", Positive: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.InsertPregen(store.Pregen{
		JobID: "pg-agent1", UserID: uid, ProfileVersion: 1,
		Positive: "p", Negative: "n", WorkflowID: "std", Width: 1344, Height: 768,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, task = claim(); task == nil || task["job_id"] != "pg-agent1" {
		t.Fatalf("idle 任务应可被领走: %+v", task)
	}
	if code, raw = agentPost("/api/v1/agent/rendered", map[string]any{
		"job_id": "pg-agent1", "ok": true, "oss_key": "u/" + uid + "/pg-agent1.jpg",
	}); code != 200 {
		t.Fatalf("pregen 回报: %d %s", code, raw)
	}
	if p, _ := ts.st.PeekPregenByJob(uid, "pg-agent1"); p == nil || !p.Ready || p.OssKey != "u/"+uid+"/pg-agent1.jpg" {
		t.Fatalf("pregen 联动: %+v", p)
	}

	// pregen 失败回报 → 任务 failed 且预生成行即刻清理(不虚增在途计数)
	if err := ts.st.CreateJob(store.Job{JobID: "pg-agent2", UserID: uid, Kind: "pregen", Priority: "idle", Positive: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.InsertPregen(store.Pregen{
		JobID: "pg-agent2", UserID: uid, ProfileVersion: 1,
		Positive: "p", WorkflowID: "std", Width: 1344, Height: 768,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, task = claim(); task == nil || task["job_id"] != "pg-agent2" {
		t.Fatalf("pg-agent2 应被领走: %+v", task)
	}
	if code, raw = agentPost("/api/v1/agent/rendered", map[string]any{
		"job_id": "pg-agent2", "ok": false, "error": "ComfyUI 崩了",
	}); code != 200 {
		t.Fatalf("失败回报: %d %s", code, raw)
	}
	if p, _ := ts.st.PeekPregenByJob(uid, "pg-agent2"); p != nil {
		t.Fatalf("失败 pregen 行应即刻清理: %+v", p)
	}

	// 8) 卡死回收: 认领后超时 → 明确失败
	if err := ts.st.CreateJob(store.Job{JobID: "j-agent3", UserID: uid, Kind: "gen", Priority: "normal", Positive: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, _, task = claim(); task == nil {
		t.Fatal("j-agent3 应被领走")
	}
	if n, err := ts.st.FailStaleRunning(time.Now().Add(time.Hour)); err != nil || n < 1 {
		t.Fatalf("卡死回收: n=%d err=%v", n, err)
	}
	if j, _ := ts.st.Job("j-agent3"); j == nil || j.Status != "failed" || !strings.Contains(j.Error, "出图机离线") {
		t.Fatalf("卡死任务应明确失败: %+v", j)
	}

	// 9) health: 代理已在线 → render_ok=true
	_, raw = ts.do(t, "GET", "/api/v1/health", "", nil)
	var henv struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &henv)
	if henv.Data["render_ok"] != true {
		t.Fatalf("代理在线 render_ok 应 true: %v", henv.Data)
	}
	if henv.Data["agent_version"] != "9.9.9" {
		t.Fatalf("agent_version 应为代理自报版本: %v", henv.Data)
	}
}

// TestAgentLatestAndGoAgent S3 版本管理: agent/latest 语义(未发布同版本/有更新全字段/
// 已最新无更新) + /go/agent 计数 302 到 Gitee 资产约定。
func TestAgentLatestAndGoAgent(t *testing.T) {
	ts := newTestServer(t, "", false)

	// 未发布 → 同版本(无更新)
	_, raw := ts.do(t, "GET", "/api/v1/agent/latest?version=1.0.0", "", nil)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.0.0" {
		t.Fatalf("未发布应返回同版本: %v", env.Data)
	}

	// 发布 1.1.0
	man := `{"version":"1.1.0","file":"aw-agent.exe","sha256":"` + strings.Repeat("ab", 32) +
		`","size":123,"notes":"测试更新","url":"https://www.cybermesh.space/go/agent"}`
	if err := os.WriteFile(ts.data+"/releases/manifest-agent.json", []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}

	// 有更新 → 全字段
	_, raw = ts.do(t, "GET", "/api/v1/agent/latest?version=1.0.0", "", nil)
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.1.0" ||
		env.Data["sha256"] != strings.Repeat("ab", 32) ||
		env.Data["url"] != "https://www.cybermesh.space/go/agent" ||
		env.Data["notes"] != "测试更新" {
		t.Fatalf("latest 字段: %v", env.Data)
	}

	// 已是最新 → 同版本无更新
	_, raw = ts.do(t, "GET", "/api/v1/agent/latest?version=1.1.0", "", nil)
	_ = json.Unmarshal(raw, &env)
	if env.Data["version"] != "1.1.0" || env.Data["notes"] != "" {
		t.Fatalf("已最新应无更新: %v", env.Data)
	}

	// /go/agent → 302 至 Gitee 资产约定 tag=agent-v{ver}/aw-agent.exe
	req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/go/agent", nil)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/go/agent 应302, got %d", resp.StatusCode)
	}
	want := "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/agent-v1.1.0/aw-agent.exe"
	if loc := resp.Header.Get("Location"); loc != want {
		t.Fatalf("Location: got %s want %s", loc, want)
	}
}

func readAll(resp *http.Response) []byte {
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.Bytes()
}
