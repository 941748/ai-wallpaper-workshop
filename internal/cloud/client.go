// Package cloud 云服务客户端: 客户端所有出站(LLM/出图/信号/更新)统一经云端代理。
// 响应包遵循 /api/v1 + {success, data, error} 规范; 云端交互(元数据)超时 3 秒,
// 出图轮询单独使用长超时客户端。离线记录写入 cloud_queue.json 补传(最多 7 天)。
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"wallpaper/internal/backend"
	"wallpaper/internal/signals"
)

// MetaTimeout 云端元数据交互超时(绝不阻塞换壁纸主线)。
const MetaTimeout = 3 * time.Second

// PlanTimeout 提示词规划超时(服务端可能同步调用 LLM 推理, 需给足时间; 实测约 12s)。
const PlanTimeout = 30 * time.Second

// QueueMaxAge 离线补传队列保留上限。
const QueueMaxAge = 7 * 24 * time.Hour

// ErrQuotaExceeded 配额用尽(429)。
var ErrQuotaExceeded = errors.New("云端配额用尽")

// Client 云服务客户端。
type Client struct {
	BaseURL string
	UserID  string
	Token   string
	Device  string

	meta *http.Client // 元数据请求(3s)
	plan *http.Client // 提示词规划(服务端 LLM 推理, 30s)
	long *http.Client // 出图轮询/下载
}

// New 创建客户端。baseURL 例 https://wallpaper.example.com。
func New(baseURL, userID, token, device string) *Client {
	return &Client{
		BaseURL: baseURL,
		UserID:  userID,
		Token:   token,
		Device:  device,
		meta:    &http.Client{Timeout: MetaTimeout},
		plan:    &http.Client{Timeout: PlanTimeout},
		long:    &http.Client{Timeout: 11 * time.Minute},
	}
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

// doJSON 发起请求并解包 envelope。
func (c *Client) doJSON(ctx context.Context, hc *http.Client, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrQuotaExceeded
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("云端 %s %s HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("云端响应解析失败: %w", err)
	}
	if !env.Success {
		if env.Error == "quota_exceeded" {
			return ErrQuotaExceeded
		}
		return fmt.Errorf("云端错误: %s", env.Error)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("云端 data 解析失败: %w", err)
		}
	}
	return nil
}

// doRaw 下载原始字节(成品图等)。
func (c *Client) doRaw(ctx context.Context, hc *http.Client, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrQuotaExceeded
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("云端 %s HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.UserID != "" {
		req.Header.Set("X-User-Id", c.UserID)
	}
}

// ---------- 注册与健康 ----------

// RegisterResp 注册结果。
type RegisterResp struct {
	UserID string `json:"user_id"`
	Token  string `json:"token"`
}

// Register 匿名注册(零注册零登录)。
func (c *Client) Register(ctx context.Context, device string) (*RegisterResp, error) {
	var out RegisterResp
	err := c.doJSON(ctx, c.meta, http.MethodPost, "/users/register",
		map[string]string{"device": device}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// HealthResp 连通性与配额预检结果。
type HealthResp struct {
	OK            bool   `json:"ok"`
	QuotaOK       bool   `json:"quota_ok"`
	ServerVersion string `json:"server_version"`
}

// Health 连通性与配额预检(tick 首步调用)。
func (c *Client) Health(ctx context.Context) (*HealthResp, error) {
	var out HealthResp
	if err := c.doJSON(ctx, c.meta, http.MethodGet, "/health", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- 提示词 ----------

// RoundItem 近期一轮出图记录(供 LLM 参考)。
type RoundItem struct {
	At     time.Time         `json:"at"`
	Combo  map[string]string `json:"combo"`
	Source string            `json:"source,omitempty"`
}

// NextReq 请求本轮提示词。
type NextReq struct {
	ProfileVersion int                `json:"profile_version"`
	Profile        map[string]float64 `json:"profile"`
	Disliked       []string           `json:"disliked,omitempty"`
	CustomKeywords []string           `json:"custom_keywords,omitempty"` // 用户自定义关键词
	RecentRounds   []RoundItem        `json:"recent_rounds"`
	PendingSignals []signals.Event    `json:"pending_signals,omitempty"`
	ScreenW        int                `json:"screen_w"`
	ScreenH        int                `json:"screen_h"`
	ForceShake     bool               `json:"force_shake"`
	DisableContext bool               `json:"disable_context,omitempty"`
}

// NextResp 云端 LLM 给出的本轮成品参数。
type NextResp struct {
	Positive   string            `json:"positive"`
	Negative   string            `json:"negative"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	WorkflowID string            `json:"workflow_id"`
	Combo      map[string]string `json:"combo,omitempty"`
	Seed       int64             `json:"seed"`
	Reason     string            `json:"reason"`
}

// NextPrompt 取本轮提示词(LLM 定 提示词+尺寸+工作流)。
// 服务端可能同步调用 LLM 推理(秒级), 不能用 meta 的 3s 超时。
func (c *Client) NextPrompt(ctx context.Context, req NextReq) (*NextResp, error) {
	var out NextResp
	if err := c.doJSON(ctx, c.plan, http.MethodPost, "/prompts/next", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PromptRecord 一轮出图记录。
type PromptRecord struct {
	At         time.Time         `json:"at"`
	Positive   string            `json:"positive"`
	Negative   string            `json:"negative,omitempty"`
	Combo      map[string]string `json:"combo,omitempty"`
	Seed       int64             `json:"seed"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	WorkflowID string            `json:"workflow_id,omitempty"`
	Source     string            `json:"source"` // cloud | pregen | local
	DurationMs int64             `json:"duration_ms"`
	Success    bool              `json:"success"`
	ProfileVer int               `json:"profile_version"`
}

// ReportPrompt 上报一轮记录。
func (c *Client) ReportPrompt(ctx context.Context, rec PromptRecord) error {
	return c.doJSON(ctx, c.meta, http.MethodPost, "/prompts", rec, nil)
}

// ReportSignals 上报漂移事件。
func (c *Client) ReportSignals(ctx context.Context, evs []signals.Event) error {
	if len(evs) == 0 {
		return nil
	}
	return c.doJSON(ctx, c.meta, http.MethodPost, "/signals", map[string]any{"events": evs}, nil)
}

// ---------- 出图 ----------

// SubmitGenerate 提交出图任务。
func (c *Client) SubmitGenerate(ctx context.Context, p backend.GenParams) (string, error) {
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/generate", p, &out); err != nil {
		return "", err
	}
	if out.JobID == "" {
		return "", errors.New("云端未返回 job_id")
	}
	return out.JobID, nil
}

// PollGenerate 查询任务状态: queued | running | done | failed。
func (c *Client) PollGenerate(ctx context.Context, jobID string) (string, error) {
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := c.doJSON(ctx, c.meta, http.MethodGet, "/generate/"+url.PathEscape(jobID), nil, &out); err != nil {
		return "", err
	}
	if out.Status == "failed" {
		return out.Status, fmt.Errorf("云端出图失败: %s", out.Error)
	}
	return out.Status, nil
}

// DownloadImage 下载成品图。
func (c *Client) DownloadImage(ctx context.Context, jobID string) ([]byte, error) {
	return c.doRaw(ctx, c.long, http.MethodGet, "/generate/"+url.PathEscape(jobID)+"/image")
}

// Backend 将云端出图封装为 backend.ImageBackend(客户端默认出图链路)。
type Backend struct {
	C          *Client
	PollPeriod time.Duration
}

// Submit 实现 backend.ImageBackend。
func (b *Backend) Submit(ctx context.Context, p backend.GenParams) (backend.JobID, error) {
	id, err := b.C.SubmitGenerate(ctx, p)
	return backend.JobID(id), err
}

// Wait 实现 backend.ImageBackend: 3 秒轮询状态, 完成后下载成品图。
func (b *Backend) Wait(ctx context.Context, id backend.JobID) ([]byte, error) {
	period := b.PollPeriod
	if period <= 0 {
		period = 3 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		status, err := b.C.PollGenerate(ctx, string(id))
		if err != nil {
			return nil, err
		}
		switch status {
		case "done":
			return b.C.DownloadImage(ctx, string(id))
		case "failed":
			return nil, fmt.Errorf("云端出图任务失败")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ---------- 空闲预生成 ----------

// PregenReq 预约下一轮空闲预生成。
type PregenReq struct {
	ProfileVersion int                `json:"profile_version"`
	Profile        map[string]float64 `json:"profile"`
	Disliked       []string           `json:"disliked,omitempty"`
	CustomKeywords []string           `json:"custom_keywords,omitempty"` // 用户自定义关键词
	RecentRounds   []RoundItem        `json:"recent_rounds"`
	ScreenW        int                `json:"screen_w"`
	ScreenH        int                `json:"screen_h"`
	DisableContext bool               `json:"disable_context,omitempty"`
}

// PregenSubmit 异步下单下一轮预生成(低优先级队列, 出图机空闲时执行)。
// 服务端下单时会同步调用 LLM 规划提示词(秒级), 同样不能用 meta 的 3s 超时。
func (c *Client) PregenSubmit(ctx context.Context, req PregenReq) error {
	return c.doJSON(ctx, c.plan, http.MethodPost, "/pregen", req, nil)
}

// PregenInfo 预生成状态与参数。
type PregenInfo struct {
	Ready          bool              `json:"ready"`
	ProfileVersion int               `json:"profile_version"`
	Positive       string            `json:"positive"`
	Negative       string            `json:"negative"`
	Combo          map[string]string `json:"combo,omitempty"`
	Seed           int64             `json:"seed"`
	WorkflowID     string            `json:"workflow_id"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
}

// PregenFetch 取预生成结果(无则 ready=false; 画像版本过期同样 ready=false)。
func (c *Client) PregenFetch(ctx context.Context, profileVersion int) (*PregenInfo, error) {
	var out PregenInfo
	path := fmt.Sprintf("/pregen?profile_version=%d", profileVersion)
	if err := c.doJSON(ctx, c.meta, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PregenImage 下载预生成成品图。
func (c *Client) PregenImage(ctx context.Context) ([]byte, error) {
	return c.doRaw(ctx, c.long, http.MethodGet, "/pregen/image")
}

// ---------- 静默自更新 ----------

// LatestResp 最新客户端信息。
type LatestResp struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Notes   string `json:"notes"`
}

// ClientLatest 查询最新客户端(有更新时 URL 非空)。
func (c *Client) ClientLatest(ctx context.Context, currentVersion string) (*LatestResp, error) {
	var out LatestResp
	path := "/client/latest?version=" + url.QueryEscape(currentVersion)
	if err := c.doJSON(ctx, c.meta, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadFile 从给定 URL 下载(仅允许自有云端域名前缀, 保证安全边界)。
func (c *Client) DownloadFile(ctx context.Context, fileURL string) ([]byte, error) {
	if len(fileURL) < len(c.BaseURL) || fileURL[:len(c.BaseURL)] != c.BaseURL {
		return nil, fmt.Errorf("拒绝非云端来源的下载: %s", fileURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.long.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ---------- 设备扫码配对 ----------

// LinkCreateResp 旧设备生成一次性配对票据。
type LinkCreateResp struct {
	Code      string `json:"code"`
	URL       string `json:"url"` // 云端 H5 中转页(手机扫码打开)
	ExpiresAt string `json:"expires_at"`
}

// LinkCreate 旧设备创建配对票据。
func (c *Client) LinkCreate(ctx context.Context) (*LinkCreateResp, error) {
	var out LinkCreateResp
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/devices/link",
		map[string]string{"action": "create", "device": c.Device}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkRedeem 新设备输入配对码领回身份。
func (c *Client) LinkRedeem(ctx context.Context, code string) (*RegisterResp, error) {
	var out RegisterResp
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/devices/link",
		map[string]string{"action": "redeem", "code": code, "device": c.Device}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- 离线补传队列 ----------

// QueuedItem 补传条目。
type QueuedItem struct {
	At      time.Time       `json:"at"`
	Kind    string          `json:"kind"` // prompt | signals
	Payload json.RawMessage `json:"payload"`
}

// LoadQueue 读取队列。
func LoadQueue(dir string) ([]QueuedItem, error) {
	var items []QueuedItem
	raw, err := readFileIfExists(dir + "/cloud_queue.json")
	if err != nil || raw == nil {
		return items, err
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Enqueue 本地记录待补传条目。
func Enqueue(dir, kind string, payload any) error {
	items, err := LoadQueue(dir)
	if err != nil {
		items = nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	items = append(items, QueuedItem{At: time.Now(), Kind: kind, Payload: b})
	return saveQueue(dir, items)
}

// FlushQueue 尽力补传队列; 只丢弃过期(>7 天)与永久失败条目, 网络失败保留待下次。
func (c *Client) FlushQueue(ctx context.Context, dir string) {
	items, err := LoadQueue(dir)
	if err != nil || len(items) == 0 {
		return
	}
	now := time.Now()
	var remain []QueuedItem
	for i, it := range items {
		if now.Sub(it.At) > QueueMaxAge {
			continue // 过期丢弃
		}
		var err error
		switch it.Kind {
		case "prompt":
			var rec PromptRecord
			if e := json.Unmarshal(it.Payload, &rec); e != nil {
				continue
			}
			err = c.ReportPrompt(ctx, rec)
		case "signals":
			var evs []signals.Event
			if e := json.Unmarshal(it.Payload, &evs); e != nil {
				continue
			}
			err = c.ReportSignals(ctx, evs)
		default:
			continue
		}
		if err != nil {
			remain = append(remain, items[i:]...) // 网络问题: 保留本条及之后, 下次重试
			break
		}
	}
	sort.SliceStable(remain, func(a, b int) bool { return remain[a].At.Before(remain[b].At) })
	_ = saveQueue(dir, remain)
}

func saveQueue(dir string, items []QueuedItem) error {
	if items == nil {
		items = []QueuedItem{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(dir+"/cloud_queue.json", data)
}

func readFileIfExists(path string) ([]byte, error) {
	b, err := osReadFile(path)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}
