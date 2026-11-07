package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"wallpaper/assets"
)

// Comfy 云服务侧的 ComfyUI 代理(出图机经内网通道接入)。
type Comfy struct {
	Base  string // 例 http://127.0.0.1:8188 或内网映射地址
	Token string
	HTTP  *http.Client

	// 出图机在线探测缓存(避免 /health 高频打爆出图机)
	mu     sync.Mutex
	pingAt time.Time
	pingOK bool
}

// 出图机探测: 单次短超时 + 结果缓存 TTL。
const (
	comfyPingTimeout = 2 * time.Second
	comfyPingTTL     = 8 * time.Second
)

// Reachable 探测出图机是否在线(GET /queue, 2s 超时), 结果缓存 8s。
// 供 /health 暴露 render_ok, 让客户端在首张壁纸前就能感知出图链路可用性。
func (c *Comfy) Reachable(ctx context.Context) bool {
	c.mu.Lock()
	if time.Since(c.pingAt) < comfyPingTTL {
		ok := c.pingOK
		c.mu.Unlock()
		return ok
	}
	c.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, comfyPingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, c.Base+"/queue", nil)
	ok := false
	if err == nil {
		c.auth(req)
		hc := &http.Client{Timeout: comfyPingTimeout}
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			ok = resp.StatusCode == http.StatusOK
		}
	}
	c.mu.Lock()
	c.pingAt, c.pingOK = time.Now(), ok
	c.mu.Unlock()
	return ok
}

// NewComfy 创建代理客户端。
func NewComfy(base, token string) *Comfy {
	return &Comfy{
		Base:  strings.TrimRight(base, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: 60 * time.Second},
	}
}

// imageRef ComfyUI 输出图像引用(/view 的入参)。
type imageRef struct {
	Filename  string
	Subfolder string
	Type      string
}

// Submit 提交工作流到 /prompt, 返回 prompt_id(不等待完成)。
// 工作流固定为 Z-Image Turbo: 仅注入提示词/尺寸/seed, 其余参数冻结在模板内。
func (c *Comfy) Submit(ctx context.Context, positive string, width, height int, seed int64) (string, error) {
	workflow, err := buildWorkflow(assets.WorkflowTemplate, positive, width, height, seed)
	if err != nil {
		return "", err
	}
	reqBody, err := json.Marshal(map[string]any{"prompt": json.RawMessage(workflow)})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/prompt", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ComfyUI 不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("ComfyUI /prompt HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.PromptID == "" {
		return "", fmt.Errorf("ComfyUI /prompt 响应异常")
	}
	return out.PromptID, nil
}

// WaitDone 轮询 /history 直到完成, 返回图像引用(只取文件名不取字节; 3s 间隔, 10 分钟超时)。
func (c *Comfy) WaitDone(ctx context.Context, promptID string) (imageRef, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		ref, done, err := c.tryHistory(ctx, promptID)
		if err != nil {
			return imageRef{}, err
		}
		if done {
			return ref, nil
		}
		select {
		case <-ctx.Done():
			return imageRef{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return imageRef{}, fmt.Errorf("ComfyUI 任务 %s 超时", promptID)
}

// FetchView 按图像引用从 /view 下载字节(兜底拉图用)。
func (c *Comfy) FetchView(ctx context.Context, ref imageRef) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", ref.Filename)
	q.Set("subfolder", ref.Subfolder)
	q.Set("type", ref.Type)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Generate 一体路径(未开 OSS 直传时的现网行为): Submit → WaitDone → FetchView。
func (c *Comfy) Generate(ctx context.Context, positive string, width, height int, seed int64) ([]byte, error) {
	pid, err := c.Submit(ctx, positive, width, height, seed)
	if err != nil {
		return nil, err
	}
	ref, err := c.WaitDone(ctx, pid)
	if err != nil {
		return nil, err
	}
	return c.FetchView(ctx, ref)
}

// tryHistory 单次 /history 查询: 完成则返回首个图像引用(网络抖动按"未完成"重试)。
func (c *Comfy) tryHistory(ctx context.Context, promptID string) (imageRef, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return imageRef{}, false, err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return imageRef{}, false, nil // 抖动重试
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return imageRef{}, false, nil
	}
	var hist map[string]struct {
		Status struct {
			Completed bool `json:"completed"`
		} `json:"status"`
		Outputs map[string]struct {
			Images []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			} `json:"images"`
		} `json:"outputs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hist); err != nil {
		return imageRef{}, false, nil
	}
	entry, ok := hist[promptID]
	if !ok {
		return imageRef{}, false, nil
	}
	for _, o := range entry.Outputs {
		for _, im := range o.Images {
			return imageRef{Filename: im.Filename, Subfolder: im.Subfolder, Type: im.Type}, true, nil
		}
	}
	return imageRef{}, false, nil
}

func (c *Comfy) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// ---------- 工作流渲染 ----------

// buildWorkflow 在 Z-Image 工作流模板上注入提示词/尺寸/seed;
// 其余参数(模型/步数/CFG/采样器)按模板原样保留, 不做任何修改。
func buildWorkflow(template []byte, positive string, width, height int, seed int64) ([]byte, error) {
	repl := strings.NewReplacer(
		"{{POSITIVE}}", jsonEscape(positive),
		"{{WIDTH}}", strconv.Itoa(width),
		"{{HEIGHT}}", strconv.Itoa(height),
		"{{SEED}}", strconv.FormatInt(seed, 10),
	)
	prep := repl.Replace(string(template))
	var js json.RawMessage
	if err := json.Unmarshal([]byte(prep), &js); err != nil {
		return nil, fmt.Errorf("工作流模板替换后不是合法 JSON: %w", err)
	}
	return []byte(prep), nil
}

// jsonEscape 转义为 JSON 字符串字面量内容(不含两侧引号, 模板中引号已存在)。
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}
