// Command aw-agent 出图机统一代理(S3): 渲染调度 + OSS 上传合并为单进程, 替代 frpc 隧道 + aw-uploader。
//
// 职责:
//  1. 轮询云端领取渲染任务(正反提示词/尺寸/seed/工作流/OSS键)
//  2. 本地工作流模板占位符注入({{POSITIVE}}/{{NEGATIVE}}/{{WIDTH}}/{{HEIGHT}}/{{SEED}})
//     → 本机 ComfyUI 出图 → JPEG 转码 → PUT OSS(服务端指定的键) → 回报
//  3. ComfyUI 看门狗: 心跳失败按 comfyui_cmd 配置自动拉起(异常时重新拉起画图服务)
//
// 配置: aw-agent.json(与 exe 同目录); 工作流模板与 exe 同目录平铺:
// {workflow_id}.json 优先, 缺省回落 default.json(可用 workflow_dir 指到别处)——
// 换模型/换工作流只改文件, 不动本程序。
// 运行: 双击 运行aw-agent.bat(窗口保持打开)。
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // ComfyUI 出图为 PNG
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	imgdraw "golang.org/x/image/draw"
)

// Version 当前代理版本(S3 版本管理: 发版时改这里, 与 Gitee tag agent-vX 对应)。
// v1.1.0: P5——按物理尺寸自算渲染尺寸(max_side 限宽)并放大回物理再上传。
const Version = "1.1.0"

type ossConf struct {
	AkID     string `json:"ak_id"`
	AkSecret string `json:"ak_secret"`
	Bucket   string `json:"bucket"`
	Endpoint string `json:"endpoint"`
}

type config struct {
	Server      string   `json:"server"`
	AgentToken  string   `json:"agent_token"`
	Oss         ossConf  `json:"oss"`
	ComfyPorts  []int    `json:"comfy_ports"`
	PollSeconds int      `json:"poll_seconds"`
	ComfyuiCmd  string   `json:"comfyui_cmd"`  // ComfyUI 启动命令(看门狗拉起用; 空=只探测报警)
	WorkflowDir string   `json:"workflow_dir"` // 工作流模板目录(空=exe 同目录 workflows)
	MaxSide     int      `json:"max_side"`     // 渲染限宽(长边, 等比): 8GB 显存=2048; 显存更大可放开(如 4096+)直出物理
}

// renderTask 云端下发的渲染任务(字段与 /api/v1/agent/claim 一致)。
type renderTask struct {
	JobID      string `json:"job_id"`
	Kind       string `json:"kind"`
	Positive   string `json:"positive"`
	Negative   string `json:"negative"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Seed       int64  `json:"seed"`
	WorkflowID string `json:"workflow_id"`
	OssKey     string `json:"oss_key"`
}

var (
	cfg        config
	exeDir     string
	exePath    string
	renderBusy atomic.Bool // 渲染中不执行自更新退出
	wfDir      string
	comfyBase  string
	httpc      = &http.Client{Timeout: 60 * time.Second}
	oldServer  bool // 服务端尚未升级(claim 404)时只提示一次
	lastProbe  time.Time
	lastLaunch time.Time
	unhealthy  int
)

func main() {
	log.SetFlags(log.Ltime)
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("定位自身路径失败: %v", err)
	}
	exePath = exe
	exeDir = filepath.Dir(exe)
	applyStagedUpdate() // 直接运行场景: 应用已暂存更新并拉起新版后退出
	raw, err := os.ReadFile(filepath.Join(exeDir, "aw-agent.json"))
	if err != nil {
		log.Fatalf("读取配置失败(aw-agent.json): %v", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("配置解析失败: %v", err)
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 5
	}
	if cfg.MaxSide <= 0 {
		cfg.MaxSide = 2048 // P5 主闸默认: 8GB 显存(4060)防爆显存+提速; 大显存卡在 aw-agent.json 配 max_side 放开
	}
	wfDir = cfg.WorkflowDir
	if wfDir == "" {
		wfDir = exeDir // 默认与 exe 同目录平铺(避免子目录混乱; 可用 workflow_dir 覆盖)
	}
	log.Printf("aw-agent v%s | 服务端 %s | OSS %s/%s | 工作流目录 %s", Version, cfg.Server, cfg.Oss.Bucket, cfg.Oss.Endpoint, wfDir)
	if cfg.ComfyuiCmd != "" {
		log.Printf("看门狗拉起命令: %s", cfg.ComfyuiCmd)
	}

	go watchdogLoop()
	go updateCheckLoop()

	for {
		if comfyBase == "" || time.Since(lastProbe) > time.Minute {
			probeComfy()
		}
		if comfyBase == "" {
			log.Printf("ComfyUI 未就绪(探 %v), %ds 后重试", cfg.ComfyPorts, cfg.PollSeconds)
			sleep()
			continue
		}
		task, err := claimTask()
		if err != nil {
			if isOldServer(err) {
				if !oldServer {
					oldServer = true
					log.Printf("服务端暂无新协议 claim 接口(待服务端升级后自动开始领任务): %v", err)
				}
			} else {
				log.Printf("领任务失败: %v", err)
			}
			sleep()
			continue
		}
		oldServer = false
		if task == nil {
			sleep()
			continue
		}

		log.Printf("领到任务 %s(%s) %dx%d wf=%s", task.JobID, task.Kind, task.Width, task.Height, task.WorkflowID)
		renderBusy.Store(true)
		err = renderAndUpload(task)
		renderBusy.Store(false)
		if err != nil {
			log.Printf("任务 %s 失败: %v", task.JobID, err)
			_ = reportRendered(task.JobID, false, "", truncate(err.Error(), 200))
			// Comfy 相关错误 → 下轮重探端口
			if strings.Contains(err.Error(), "ComfyUI") {
				comfyBase = ""
			}
			sleep()
			continue
		}
		if err := reportRendered(task.JobID, true, task.OssKey, ""); err != nil {
			// 回报失败: 任务留在服务端由超时回收明确失败, 客户端会重试
			log.Printf("任务 %s 完成但回报失败(服务端将超时回收): %v", task.JobID, err)
		} else {
			log.Printf("任务 %s -> %s 渲染上传完成", task.JobID, task.OssKey)
		}
	}
}

func sleep() { time.Sleep(time.Duration(cfg.PollSeconds) * time.Second) }

// ---------- ComfyUI 探测与看门狗 ----------

func probeComfy() {
	lastProbe = time.Now()
	for _, p := range cfg.ComfyPorts {
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/queue", nil)
		resp, err := httpc.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if comfyBase != base {
					log.Printf("ComfyUI 就绪: %s", base)
				}
				comfyBase = base
				unhealthy = 0
				return
			}
		}
	}
	comfyBase = ""
}

// watchdogLoop 心跳看门狗: 连续不健康且距上次拉起超冷却期 → 按配置命令重新拉起 ComfyUI。
func watchdogLoop() {
	for {
		time.Sleep(30 * time.Second)
		probeComfy()
		if comfyBase != "" {
			unhealthy = 0
			continue
		}
		unhealthy++
		if unhealthy < 2 || cfg.ComfyuiCmd == "" {
			continue
		}
		if time.Since(lastLaunch) < 10*time.Minute {
			continue // 冷却: 防止重复拉起堆进程
		}
		lastLaunch = time.Now()
		log.Printf("ComfyUI 连续不健康, 按配置拉起: %s", cfg.ComfyuiCmd)
		cmd := exec.Command("cmd", "/C", cfg.ComfyuiCmd)
		// 工作目录固定为 exe 所在目录: comfyui_cmd 填同目录批处理文件名即可, 与启动方式无关
		cmd.Dir = exeDir
		// 分离进程: 看门狗不阻塞, ComfyUI 独立于本窗口存活
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		if err := cmd.Start(); err != nil {
			log.Printf("拉起失败: %v", err)
			continue
		}
		_ = cmd.Process.Release()
		log.Printf("拉起命令已发出(pid=%d), 等待就绪", cmd.Process.Pid)
	}
}

// ---------- 云端协议 ----------

func agentPost(path string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, cfg.Server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("服务端不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("旧版服务端: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s HTTP %d: %s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("响应解析: %w", err)
	}
	if !env.Success {
		return fmt.Errorf("服务端错误: %s", env.Error)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("data 解析: %w", err)
		}
	}
	return nil
}

func isOldServer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "旧版服务端")
}

// claimTask 领取渲染任务; 无任务返回 (nil, nil)。
func claimTask() (*renderTask, error) {
	var out struct {
		Task *renderTask `json:"task"`
	}
	if err := agentPost("/api/v1/agent/claim", map[string]any{"version": Version}, &out); err != nil {
		return nil, err
	}
	return out.Task, nil
}

// reportRendered 回报渲染结果(成功时回传 claim 下发的 oss_key 供服务端核对)。
func reportRendered(jobID string, ok bool, ossKey, errMsg string) error {
	payload := map[string]any{"job_id": jobID, "ok": ok, "oss_key": ossKey}
	if !ok {
		payload["error"] = errMsg
	}
	return agentPost("/api/v1/agent/rendered", payload, nil)
}

// ---------- 工作流模板(本地文件, 换模型不动代码) ----------

// loadTemplate 按 workflow_id 取模板: workflows/{id}.json → 回落 default.json。
func loadTemplate(workflowID string) ([]byte, error) {
	if workflowID != "" {
		if b, err := os.ReadFile(filepath.Join(wfDir, workflowID+".json")); err == nil {
			return b, nil
		}
	}
	b, err := os.ReadFile(filepath.Join(wfDir, "default.json"))
	if err != nil {
		return nil, fmt.Errorf("工作流模板缺失(%s.json 与 default.json 均不在 %s): %w", workflowID, wfDir, err)
	}
	return b, nil
}

// injectWorkflow 占位符注入(与原服务端 buildWorkflow 同口径; {{NEGATIVE}} 为 S3 新增,
// 现网 Z-Image 模板无此占位符则原样保留, 未来 cfg>1 的工作流放上即生效)。
func injectWorkflow(tmpl []byte, t *renderTask, rw, rh int) ([]byte, error) {
	repl := strings.NewReplacer(
		"{{POSITIVE}}", jsonEscape(t.Positive),
		"{{NEGATIVE}}", jsonEscape(t.Negative),
		"{{WIDTH}}", fmt.Sprintf("%d", rw),
		"{{HEIGHT}}", fmt.Sprintf("%d", rh),
		"{{SEED}}", fmt.Sprintf("%d", t.Seed),
	)
	out := repl.Replace(string(tmpl))
	var js json.RawMessage
	if err := json.Unmarshal([]byte(out), &js); err != nil {
		return nil, fmt.Errorf("模板替换后不是合法 JSON: %w", err)
	}
	return []byte(out), nil
}

// jsonEscape 转义为 JSON 字符串字面量内容(不含两侧引号)。
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// ---------- ComfyUI 执行 ----------

// renderAndUpload 模板注入 → 提交 → 等完成 → 取图 → 放大回物理 → JPEG 转码 → PUT OSS。
// P5: claim 下发 width/height=物理交付尺寸; 本机按 max_side 等比自算渲染尺寸(8倍数),
// 渲染后放大回物理再上传——限宽职责在出图机(显存是它自己的资源), 服务端只管要什么尺寸。
func renderAndUpload(t *renderTask) error {
	tmpl, err := loadTemplate(t.WorkflowID)
	if err != nil {
		return err
	}
	rw, rh := renderSize(t.Width, t.Height, cfg.MaxSide)
	if rw != t.Width || rh != t.Height {
		log.Printf("job=%s 物理%dx%d → 渲染%dx%d(max_side=%d)", t.JobID, t.Width, t.Height, rw, rh, cfg.MaxSide)
	}
	graph, err := injectWorkflow(tmpl, t, rw, rh)
	if err != nil {
		return err
	}
	pid, err := submitPrompt(graph)
	if err != nil {
		return err
	}
	ref, err := waitDone(pid)
	if err != nil {
		return err
	}
	raw, err := fetchView(ref)
	if err != nil {
		return err
	}
	data := raw
	if jpg, ok := toJPEG(raw, t.Width, t.Height); ok {
		data = jpg
	}
	return putOSS(t.OssKey, data)
}

// renderSize 物理尺寸 → 渲染尺寸: 长边等比限宽到 max_side, 8 倍数(ComfyUI latent 要求);
// 不超限则 round8(物理)。放大回物理由 toJPEG 负责。
func renderSize(physW, physH, maxSide int) (int, int) {
	if physW < 320 || physH < 320 { // 防脏值(渲染兜底 16:9)
		physW, physH = 1920, 1080
	}
	w, h := physW, physH
	if m := maxSide; m >= 320 && (w > m || h > m) {
		r := float64(m) / float64(w)
		if r2 := float64(m) / float64(h); r2 < r {
			r = r2
		}
		w, h = int(float64(w)*r+0.5), int(float64(h)*r+0.5)
	}
	return round8(w), round8(h)
}

func round8(v int) int {
	n := (v + 4) / 8 * 8
	if n < 8 {
		n = 8
	}
	return n
}

func submitPrompt(graph []byte) (string, error) {
	payload := map[string]any{"prompt": json.RawMessage(graph), "client_id": "aw-agent"}
	body, _ := json.Marshal(payload)
	resp, err := httpc.Post(comfyBase+"/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ComfyUI /prompt: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ComfyUI /prompt HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.PromptID == "" {
		return "", fmt.Errorf("ComfyUI 未返回 prompt_id: %s", truncate(string(raw), 300))
	}
	return out.PromptID, nil
}

type comfyRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// waitDone 轮询 history 直到出图(3s 间隔, 10 分钟上限)。
func waitDone(pid string) (*comfyRef, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		resp, err := httpc.Get(comfyBase + "/history/" + url.PathEscape(pid))
		if err != nil {
			return nil, fmt.Errorf("ComfyUI /history: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var hist map[string]struct {
			Outputs map[string]struct {
				Images []comfyRef `json:"images"`
			} `json:"outputs"`
		}
		if err := json.Unmarshal(raw, &hist); err != nil {
			continue
		}
		entry, ok := hist[pid]
		if !ok {
			continue
		}
		for _, o := range entry.Outputs {
			if len(o.Images) > 0 {
				ref := o.Images[0]
				return &ref, nil
			}
		}
	}
	return nil, fmt.Errorf("ComfyUI 渲染超时(10 分钟)")
}

func fetchView(ref *comfyRef) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", ref.Filename)
	q.Set("subfolder", ref.Subfolder)
	q.Set("type", ref.Type)
	resp, err := httpc.Get(comfyBase + "/view?" + q.Encode())
	if err != nil {
		return nil, fmt.Errorf("ComfyUI /view: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ComfyUI /view HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("ComfyUI /view 空响应")
	}
	return b, nil
}

// putOSS V1 header 签名 PUT(虚拟主机风格; 签名资源带桶名; Content-MD5/Type 参与签名)。
func putOSS(key string, data []byte) error {
	sum := md5.Sum(data)
	md5b64 := base64.StdEncoding.EncodeToString(sum[:])
	date := time.Now().UTC().Format(http.TimeFormat)
	resource := "/" + cfg.Oss.Bucket + "/" + key
	sts := strings.Join([]string{"PUT", md5b64, "image/jpeg", date, resource}, "\n")
	mac := hmac.New(sha1.New, []byte(cfg.Oss.AkSecret))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	putURL := fmt.Sprintf("https://%s.%s/%s", cfg.Oss.Bucket, cfg.Oss.Endpoint, strings.Join(segs, "/"))
	req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Content-MD5", md5b64)
	req.Header.Set("Authorization", "OSS "+cfg.Oss.AkID+":"+sig)
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("PUT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// toJPEG 转码压缩(q90, 与原服务端/现网口径一致)。
func toJPEG(src []byte, dstW, dstH int) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false
	}
	// P5: 渲染尺寸(≤max_side)放大回物理交付尺寸; 尺寸一致(含 round8 后相同)则直转不缩放
	if dstW >= 320 && dstH >= 320 && (img.Bounds().Dx() != dstW || img.Bounds().Dy() != dstH) {
		dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
		imgdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------- 自更新(S3 版本管理): 启动即查 + 每 6 小时, 空闲才退出换新 ----------

func updateCheckLoop() {
	checkForUpdate()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		checkForUpdate()
	}
}

func checkForUpdate() {
	if renderBusy.Load() {
		return // 渲染任务进行中不打断, 下轮再查
	}
	var latest struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		SHA256  string `json:"sha256"`
		Notes   string `json:"notes"`
	}
	if err := agentGet("/api/v1/agent/latest?version="+url.QueryEscape(Version), &latest); err != nil || latest.Version == "" {
		return // 旧服务端无此接口 / 无更新 → 静默(部署顺序容错)
	}
	if compareVer(latest.Version, Version) <= 0 {
		return
	}
	log.Printf("发现新版本 %s(当前 %s): %s", latest.Version, Version, latest.Notes)
	if err := stageUpdate(latest.URL, latest.SHA256); err != nil {
		log.Printf("更新暂存失败(6 小时后重试): %v", err)
		return
	}
	log.Printf("新版本已暂存 aw-agent.new.exe, 退出交由启动器换新重启")
	os.Exit(0)
}

// agentGet 带鉴权的 GET(信封解析与 agentPost 同口径)。
func agentGet(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, cfg.Server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("服务端不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s HTTP %d", path, resp.StatusCode)
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		return fmt.Errorf("响应异常: %s", truncate(string(raw), 200))
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// stageUpdate 下载 → sha256 校验 → 暂存 aw-agent.new.exe(运行中的 exe 不能直接覆盖)。
func stageUpdate(fileURL, wantSHA string) error {
	resp, err := httpc.Get(fileURL)
	if err != nil {
		return fmt.Errorf("下载: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	tmp := filepath.Join(exeDir, "aw-agent.dl")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	f.Close()
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 不匹配(得到 %s…, 期望 %s…)", truncate(got, 12), truncate(wantSHA, 12))
	}
	newPath := filepath.Join(exeDir, "aw-agent.new.exe")
	_ = os.Remove(newPath)
	return os.Rename(tmp, newPath)
}

// applyStagedUpdate 启动时应用已暂存更新(直接运行 exe、未经启动器换新的场景):
// 当前文件改名 .old → 新文件就位 → 拉起新版进程后自身退出。
func applyStagedUpdate() {
	newPath := filepath.Join(exeDir, "aw-agent.new.exe")
	if _, err := os.Stat(newPath); err != nil {
		return
	}
	oldPath := filepath.Join(exeDir, "aw-agent.old.exe")
	_ = os.Remove(oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		log.Printf("更新换名失败: %v", err)
		return
	}
	if err := os.Rename(newPath, exePath); err != nil {
		log.Printf("更新就位失败: %v", err)
		return
	}
	log.Printf("已应用暂存的更新, 拉起新版...")
	cmd := exec.Command(exePath)
	cmd.Dir = exeDir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED | NEW_GROUP
	if err := cmd.Start(); err != nil {
		log.Printf("拉起新版失败, 请手动运行 %s: %v", exePath, err)
		return
	}
	_ = cmd.Process.Release()
	os.Exit(0)
}

// compareVer 语义化版本比较(a>b→1, 相等→0); 与服务端同口径。
func compareVer(a, b string) int {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}
