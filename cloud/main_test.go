package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"wallpaper/cloud/llm"
	"wallpaper/cloud/store"
	"wallpaper/internal/probe"
)

// ---------- mock ComfyUI ----------

func gradientPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y*3) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newMockComfy(t *testing.T) *httptest.Server {
	pngData := gradientPNG(t, 1344, 768)
	mux := http.NewServeMux()
	promptID := "p1"
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("lora_name")) {
			t.Logf("workflow contains lora node")
		}
		_, _ = w.Write([]byte(`{"prompt_id":"` + promptID + `"}`))
	})
	mux.HandleFunc("/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"p1":{"status":{"completed":true},"outputs":{"9":{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}}}}`))
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pngData)
	})
	return httptest.NewServer(mux)
}

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
	for _, sub := range []string{"probes", "images", "pregen", "releases"} {
		if err := os.MkdirAll(data+"/"+sub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(data + "/cloud.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	comfyMock := newMockComfy(t)
	t.Cleanup(comfyMock.Close)

	llmClient := llm.New(llmBase, "", "mock")
	if withLLM {
		llmClient.Key = "test-key"
	}
	reg := DefaultRegistry()
	q := &queue{st: st, comfy: NewComfy(comfyMock.URL, ""), reg: reg, dataDir: data,
		logf: func(string, ...any) {}, idleDelay: time.Millisecond}
	api := &API{st: st, llm: llmClient, reg: reg, q: q, dataDir: data,
		version: "test", logf: func(string, ...any) {},
		genHour: 6, genDay: 40, llmHour: 8, llmDay: 60}

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
	status, jobErr := waitJob(t, ts, token, genResp.JobID, 10*time.Second)
	if status != "done" {
		t.Fatalf("job status = %s (err=%s)", status, jobErr)
	}
	resp, imgRaw := ts.do(t, "GET", "/api/v1/generate/"+genResp.JobID+"/image", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("image status %d", resp.StatusCode)
	}
	if _, err := png.Decode(bytes.NewReader(imgRaw)); err != nil {
		t.Fatalf("image not png: %v", err)
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
	deadline := time.Now().Add(10 * time.Second)
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

	// 探针池接口
	resp, raw = ts.do(t, "GET", "/api/v1/probes/sample?count=12", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("probe sample %d", resp.StatusCode)
	}
	var pSample struct {
		Items []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"items"`
	}
	decodeData(t, raw, &pSample)
	if len(pSample.Items) != 12 {
		t.Fatalf("want 12 probe items, got %d", len(pSample.Items))
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

func TestBuildWorkflow(t *testing.T) {
	raw, err := buildWorkflow([]byte(templateFixture), `POS "quoted"`, 1920, 1080, 42)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Fatalf("placeholder not replaced: %s", raw)
	}
	var nodes map[string]struct {
		ClassType string                     `json:"class_type"`
		Inputs    map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &nodes); err != nil {
		t.Fatal(err)
	}
	// 提示词/尺寸/seed 注入
	var positive string
	if err := json.Unmarshal(nodes["57:27"].Inputs["text"], &positive); err != nil || positive != `POS "quoted"` {
		t.Fatalf("positive not applied: %q (err=%v)", positive, err)
	}
	var width, height float64
	_ = json.Unmarshal(nodes["57:13"].Inputs["width"], &width)
	_ = json.Unmarshal(nodes["57:13"].Inputs["height"], &height)
	if width != 1920 || height != 1080 {
		t.Fatalf("size not applied: %vx%v", width, height)
	}
	var seed float64
	_ = json.Unmarshal(nodes["57:3"].Inputs["seed"], &seed)
	if seed != 42 {
		t.Fatalf("seed not applied: %v", seed)
	}
	// 其余参数冻结: 步数/CFG/采样器保持模板原值
	var steps, cfg float64
	var sampler, scheduler string
	_ = json.Unmarshal(nodes["57:3"].Inputs["steps"], &steps)
	_ = json.Unmarshal(nodes["57:3"].Inputs["cfg"], &cfg)
	_ = json.Unmarshal(nodes["57:3"].Inputs["sampler_name"], &sampler)
	_ = json.Unmarshal(nodes["57:3"].Inputs["scheduler"], &scheduler)
	if steps != 8 || cfg != 1 || sampler != "res_multistep" || scheduler != "simple" {
		t.Fatalf("frozen params modified: steps=%v cfg=%v sampler=%q scheduler=%q", steps, cfg, sampler, scheduler)
	}
	var unet string
	_ = json.Unmarshal(nodes["57:28"].Inputs["unet_name"], &unet)
	if unet != "z_image_turbo_int8_convrot.safetensors" {
		t.Fatalf("unet should stay frozen: %q", unet)
	}
}

// templateFixture 与 assets/workflow_template.json 结构一致的测试模板(占位值会被 buildWorkflow 覆盖)。
const templateFixture = `{
  "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "z-image-turbo", "images": ["57:8", 0]}},
  "57:30": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen_3_4b_fp4_mixed.safetensors", "type": "lumina2"}},
  "57:29": {"class_type": "VAELoader", "inputs": {"vae_name": "ae.safetensors"}},
  "57:33": {"class_type": "ConditioningZeroOut", "inputs": {"conditioning": ["57:27", 0]}},
  "57:8": {"class_type": "VAEDecode", "inputs": {"samples": ["57:3", 0], "vae": ["57:29", 0]}},
  "57:28": {"class_type": "UNETLoader", "inputs": {"unet_name": "z_image_turbo_int8_convrot.safetensors"}},
  "57:27": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{POSITIVE}}", "clip": ["57:30", 0]}},
  "57:13": {"class_type": "EmptySD3LatentImage", "inputs": {"width": {{WIDTH}}, "height": {{HEIGHT}}, "batch_size": 1}},
  "57:11": {"class_type": "ModelSamplingAuraFlow", "inputs": {"shift": 3, "sampling": "flow", "model": ["57:28", 0]}},
  "57:3": {"class_type": "KSampler", "inputs": {"seed": {{SEED}}, "steps": 8, "cfg": 1, "sampler_name": "res_multistep", "scheduler": "simple", "denoise": 1, "model": ["57:11", 0], "positive": ["57:27", 0], "negative": ["57:33", 0], "latent_image": ["57:13", 0]}}
}`

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

// ---------- 探针池 ----------

func TestProbePoolRetriesFailed(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(data+"/probes", 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	q := &queue{st: st, reg: DefaultRegistry(), dataDir: data, logf: func(string, ...any) {}}

	q.ensureProbePool()
	id := "probe-" + probe.BalancedCombos(probe.FirstCount)[0].ID
	j, err := st.Job(id)
	if err != nil || j == nil {
		t.Fatalf("探针任务应已创建: %v", err)
	}
	if j.Status != "queued" {
		t.Fatalf("初始状态应为 queued, got %s", j.Status)
	}

	// 模拟出图失败(如出图机离线)后重启补池: 应重新排为 queued
	_ = st.SetJobStatus(id, "failed", "comfy down", "")
	q.ensureProbePool()
	j2, _ := st.Job(id)
	if j2 == nil || j2.Status != "queued" {
		t.Fatalf("失败探针任务应重新排队, got %+v", j2)
	}
}
