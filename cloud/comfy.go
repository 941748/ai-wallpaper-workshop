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
	"time"

	"wallpaper/assets"
)

// Comfy 云服务侧的 ComfyUI 代理(出图机经内网通道接入)。
type Comfy struct {
	Base string // 例 http://127.0.0.1:8188 或内网映射地址
	Token string
	HTTP  *http.Client
}

// NewComfy 创建代理客户端。
func NewComfy(base, token string) *Comfy {
	return &Comfy{
		Base:  strings.TrimRight(base, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Generate 执行一次出图: 渲染工作流 → /prompt → 轮询 /history → /view 下载。
// 工作流固定为 Z-Image Turbo: 仅注入提示词/尺寸/seed, 其余参数冻结在模板内。
func (c *Comfy) Generate(ctx context.Context, positive string, width, height int, seed int64) ([]byte, error) {
	workflow, err := buildWorkflow(assets.WorkflowTemplate, positive, width, height, seed)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(map[string]any{"prompt": json.RawMessage(workflow)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/prompt", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ComfyUI 不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("ComfyUI /prompt HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.PromptID == "" {
		return nil, fmt.Errorf("ComfyUI /prompt 响应异常")
	}

	// 轮询(3s 间隔, 总超时 10 分钟)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		img, done, err := c.tryFetch(ctx, out.PromptID)
		if err != nil {
			return nil, err
		}
		if done {
			return img, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil, fmt.Errorf("ComfyUI 任务 %s 超时", out.PromptID)
}

func (c *Comfy) tryFetch(ctx context.Context, promptID string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, false, err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, nil // 抖动重试
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, nil
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
		return nil, false, nil
	}
	entry, ok := hist[promptID]
	if !ok {
		return nil, false, nil
	}
	for _, o := range entry.Outputs {
		for _, im := range o.Images {
			q := url.Values{}
			q.Set("filename", im.Filename)
			q.Set("subfolder", im.Subfolder)
			q.Set("type", im.Type)
			req2, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/view?"+q.Encode(), nil)
			if err != nil {
				return nil, false, err
			}
			c.auth(req2)
			resp2, err := c.HTTP.Do(req2)
			if err != nil {
				return nil, false, err
			}
			data, err := io.ReadAll(resp2.Body)
			resp2.Body.Close()
			if err != nil {
				return nil, false, err
			}
			return data, true, nil
		}
	}
	return nil, false, nil
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
