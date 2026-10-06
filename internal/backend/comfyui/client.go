// Package comfyui 实现直连 ComfyUI 的调试后端(默认关闭, 仅局域网联调与应急)。
// 协议: POST /prompt(API 格式工作流, 占位符替换) → 轮询 /history/{id} → GET /view 下载。
// 全程 HTTP 轮询, 不依赖 WebSocket(反代无需额外配置)。
// 工作流固定为 Z-Image Turbo(z_image_wallpaper): 仅注入提示词/尺寸/seed, 其余参数冻结。
package comfyui

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
	"time"

	"wallpaper/assets"
	"wallpaper/internal/backend"
)

// 默认参数。
const (
	DefaultPollInterval = 3 * time.Second
	DefaultTimeout      = 10 * time.Minute
)

// Client ComfyUI 直连客户端。
type Client struct {
	BaseURL      string // 例 http://127.0.0.1:8188
	Token        string // 可选 Bearer
	Template     []byte // API 格式工作流模板; 空 = 使用内嵌默认模板
	PollInterval time.Duration
	Timeout      time.Duration
	HTTP         *http.Client
}

// New 创建直连客户端。
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Token:        token,
		PollInterval: DefaultPollInterval,
		Timeout:      DefaultTimeout,
		HTTP:         &http.Client{Timeout: 60 * time.Second},
	}
}

// Submit 渲染工作流并提交任务。
func (c *Client) Submit(ctx context.Context, p backend.GenParams) (backend.JobID, error) {
	body, err := c.render(p)
	if err != nil {
		return "", err
	}
	reqBody, err := json.Marshal(map[string]any{"prompt": json.RawMessage(body)})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/prompt", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("comfyui /prompt HTTP %d: %s", resp.StatusCode, snippet(resp.Body))
	}
	var out struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("comfyui /prompt 响应解析失败: %w", err)
	}
	if out.PromptID == "" {
		return "", fmt.Errorf("comfyui /prompt 未返回 prompt_id")
	}
	return backend.JobID(out.PromptID), nil
}

// Wait 轮询任务完成并下载成品图(3s 间隔, 兜底超时 10min)。
func (c *Client) Wait(ctx context.Context, id backend.JobID) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := c.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		img, done, err := c.tryFetch(ctx, id)
		if err != nil {
			return nil, err
		}
		if done {
			return img, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("comfyui 任务 %s 超时未完成", id)
		case <-ticker.C:
		}
	}
}

// tryFetch 查询一次 history; 完成时下载图片返回 (img, true, nil)。
func (c *Client) tryFetch(ctx context.Context, id backend.JobID) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/history/"+url.PathEscape(string(id)), nil)
	if err != nil {
		return nil, false, err
	}
	c.auth(req)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, false, nil // 网络抖动: 下轮重试
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, nil
	}

	var hist map[string]struct {
		Outputs map[string]struct {
			Images []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			} `json:"images"`
		} `json:"outputs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hist); err != nil {
		return nil, false, nil
	}
	entry, ok := hist[string(id)]
	if !ok {
		return nil, false, nil // 尚未完成
	}
	for _, out := range entry.Outputs {
		for _, im := range out.Images {
			data, err := c.download(ctx, im.Filename, im.Subfolder, im.Type)
			if err != nil {
				return nil, false, err
			}
			return data, true, nil
		}
	}
	return nil, false, fmt.Errorf("comfyui 任务 %s 完成但无输出图片", id)
}

// download 通过 /view 下载成品图。
func (c *Client) download(ctx context.Context, filename, subfolder, typ string) ([]byte, error) {
	if typ == "" {
		typ = "output"
	}
	q := url.Values{}
	q.Set("filename", filename)
	q.Set("subfolder", subfolder)
	q.Set("type", typ)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comfyui /view HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// render 将模板占位符替换为实际参数(字符串做 JSON 转义, 数字直接替换)。
// 按约定仅注入提示词/尺寸/seed, 工作流其余参数(模型/步数/CFG/采样器)原样保留。
func (c *Client) render(p backend.GenParams) ([]byte, error) {
	tmpl := c.Template
	if len(tmpl) == 0 {
		tmpl = assets.WorkflowTemplate
	}
	seed := p.Seed
	if seed <= 0 {
		seed = time.Now().UnixNano() & 0x7fffffffffffffff
	}
	repl := strings.NewReplacer(
		"{{POSITIVE}}", jsonEscape(p.Positive),
		"{{WIDTH}}", strconv.Itoa(p.Width),
		"{{HEIGHT}}", strconv.Itoa(p.Height),
		"{{SEED}}", strconv.FormatInt(seed, 10),
	)
	out := repl.Replace(string(tmpl))
	var js json.RawMessage
	if err := json.Unmarshal([]byte(out), &js); err != nil {
		return nil, fmt.Errorf("工作流模板替换后不是合法 JSON: %w", err)
	}
	return []byte(out), nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// jsonEscape 返回 JSON 字符串内部的转义内容(不含引号)。
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 256))
	return strings.TrimSpace(string(b))
}
