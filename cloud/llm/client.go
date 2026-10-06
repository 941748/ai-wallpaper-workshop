// Package llm OpenAI 兼容大模型客户端(默认小米 MiMo; 密钥仅存服务端)。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client LLM 客户端。
type Client struct {
	Base  string // 例 https://api.deepseek.com
	Key   string
	Model string // 例 deepseek-chat
	HTTP  *http.Client
}

// New 创建客户端。
func New(base, key, model string) *Client {
	return &Client{
		Base:  strings.TrimRight(base, "/"),
		Key:   key,
		Model: model,
		HTTP:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Enabled 未配置密钥时禁用 LLM(服务端回退规则引擎)。
func (c *Client) Enabled() bool { return c != nil && c.Key != "" && c.Base != "" }

type chatReq struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type respFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatJSON 调用 chat/completions 并返回内容(要求 JSON 输出)。
func (c *Client) ChatJSON(ctx context.Context, system, user string) (string, error) {
	reqBody := chatReq{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature:    0.6,
		// 推理模型思维链与 JSON 输出共用上限: 实测常规约 600, 复杂场景预估 ≤3K; 8192 留足余量
		MaxTokens:      8192,
		ResponseFormat: &respFormat{Type: "json_object"},
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("LLM 响应解析失败: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("LLM 错误: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM 无返回内容")
	}
	return out.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
