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
	"strings"
	"time"

	"wallpaper/internal/backend"
	"wallpaper/internal/policy"
	"wallpaper/internal/signals"
)

// MetaTimeout 云端元数据交互超时(绝不阻塞换壁纸主线)。
const MetaTimeout = 3 * time.Second

// PlanTimeout 提示词规划超时(服务端可能同步调用 LLM 推理, 需给足时间;
// 实测约 18s, 服务端 LLM 预算 50s, 客户端需更大以免提前断开导致降级)。
const PlanTimeout = 60 * time.Second

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
	plan *http.Client // 提示词规划(服务端 LLM 推理, 60s)
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
// DeviceInfoPc PC 上报的设备属性(注册/配对随行; 服务端落 devices 表, 配对列表显示厂商+型号)。
type DeviceInfoPc struct {
	Type         string `json:"type"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	OS           string `json:"os"`
	AppVersion   string `json:"app_version"`
}

// NewPCDeviceInfo 组装 PC 设备属性(厂商/型号由调用方从系统读取, desktop.SystemManufacturerModel)。
func NewPCDeviceInfo(manufacturer, model, appVersion string) *DeviceInfoPc {
	return &DeviceInfoPc{
		Type: "pc", Model: model, Manufacturer: manufacturer,
		OS: "Windows", AppVersion: appVersion,
	}
}

func (c *Client) Register(ctx context.Context, device string, info *DeviceInfoPc) (*RegisterResp, error) {
	var out RegisterResp
	payload := map[string]any{"device": device}
	if info != nil {
		payload["device_info"] = info
	}
	err := c.doJSON(ctx, c.meta, http.MethodPost, "/users/register", payload, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// HealthResp 连通性与配额预检结果。
type HealthResp struct {
	OK            bool   `json:"ok"`
	QuotaOK       bool   `json:"quota_ok"`
	RenderOK      bool   `json:"render_ok"` // 出图机是否在线(服务端探测)
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
	Packs          []string           `json:"packs,omitempty"`           // 选中的风格包名(一图一包)
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
	OnStage    func(stage string) // 阶段回调: Wait 进入轮询="render", 开始下载="down"; nil 则不回调
}

// Submit 实现 backend.ImageBackend。
func (b *Backend) Submit(ctx context.Context, p backend.GenParams) (backend.JobID, error) {
	id, err := b.C.SubmitGenerate(ctx, p)
	return backend.JobID(id), err
}

// Wait 实现 backend.ImageBackend: 3 秒轮询状态, 完成后下载成品图。
func (b *Backend) Wait(ctx context.Context, id backend.JobID) ([]byte, error) {
	if b.OnStage != nil {
		b.OnStage("render")
	}
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
			if b.OnStage != nil {
				b.OnStage("down")
			}
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

// Reachable 实现 backend.ImageBackend: 经云端 /health 的 render_ok 感知出图机是否在线。
func (b *Backend) Reachable(ctx context.Context) bool {
	h, err := b.C.Health(ctx)
	return err == nil && h.RenderOK
}

// ---------- 空闲预生成 ----------

// PregenReq 预约下一轮空闲预生成。
type PregenReq struct {
	ProfileVersion int                `json:"profile_version"`
	Profile        map[string]float64 `json:"profile"`
	Disliked       []string           `json:"disliked,omitempty"`
	CustomKeywords []string           `json:"custom_keywords,omitempty"` // 用户自定义关键词
	Packs          []string           `json:"packs,omitempty"`           // 选中的风格包名(一图一包)
	RecentRounds   []RoundItem        `json:"recent_rounds"`
	ScreenW        int                `json:"screen_w"`
	ScreenH        int                `json:"screen_h"`
	DisableContext bool               `json:"disable_context,omitempty"`
}

// PregenSubmit 异步下单预生成(低优先级队列, 出图机空闲时执行), 返回任务 ID。
// 服务端下单时会同步调用 LLM 规划提示词(秒级), 同样不能用 meta 的 3s 超时。
func (c *Client) PregenSubmit(ctx context.Context, req PregenReq) (string, error) {
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := c.doJSON(ctx, c.plan, http.MethodPost, "/pregen", req, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// PregenInfo 预生成状态与参数。
type PregenInfo struct {
	Ready          bool              `json:"ready"`
	ProfileVersion int               `json:"profile_version"`
	JobID          string            `json:"job_id"` // 就绪单 ID(提货用)
	Positive       string            `json:"positive"`
	Negative       string            `json:"negative"`
	Combo          map[string]string `json:"combo,omitempty"`
	Seed           int64             `json:"seed"`
	WorkflowID     string            `json:"workflow_id"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
}

// PregenFetch 取预生成结果(无则 ready=false); jobID 非空时按单精确查询(补池多单场景)。
func (c *Client) PregenFetch(ctx context.Context, profileVersion int, jobID string) (*PregenInfo, error) {
	var out PregenInfo
	path := fmt.Sprintf("/pregen?profile_version=%d", profileVersion)
	if jobID != "" {
		path += "&job_id=" + jobID
	}
	if err := c.doJSON(ctx, c.meta, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PregenImage 下载预生成成品图; jobID 非空时按单提货(服务端提走即删)。
func (c *Client) PregenImage(ctx context.Context, jobID string) ([]byte, error) {
	path := "/pregen/image"
	if jobID != "" {
		path += "?job_id=" + jobID
	}
	return c.doRaw(ctx, c.long, http.MethodGet, path)
}

// ---------- 运营策略 ----------

// Policy 拉取运营策略(活跃时段/备用池目标/暂停选项; 失败由调用方回退本地缓存)。
func (c *Client) Policy(ctx context.Context) (*policy.Policy, error) {
	var out policy.Policy
	if err := c.doJSON(ctx, c.meta, http.MethodGet, "/policy", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
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

// Dict 服务端词典下行(GET /profile 的 dict 字段; 安卓与 PC 偏好页同源数据源)。
type Dict struct {
	Dimensions []DictDim  `json:"dimensions"`
	Packs      []DictPack `json:"packs"`
}

// DictDim 一个维度(id/中文名/词条)。
type DictDim struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Values []DictVal `json:"values"`
}

// DictVal 一个词条(英文键 + 中文名)。
type DictVal struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DictPack 一个风格包(名称 + 覆盖词条 "dim/val")。
type DictPack struct {
	Name   string   `json:"name"`
	Covers []string `json:"covers"`
}

// RemoteProfile 服务端画像快照(双端同步下行; packs 三态: nil=推断/[]=清空/列表=选中)。
type RemoteProfile struct {
	ProfileVersion int                `json:"profile_version"`
	Profile        map[string]float64 `json:"profile"`
	Disliked       []string           `json:"disliked"`
	Custom         []string           `json:"custom"`
	Packs          []string           `json:"packs"`
	Dict           *Dict              `json:"dict"` // 词典下行(PC 偏好页数据源, 与安卓同源)
}

// GetProfile 拉取服务端画像快照(GET /api/v1/profile)。
func (c *Client) GetProfile(ctx context.Context) (*RemoteProfile, error) {
	var out RemoteProfile
	// 注意: doJSON 已拼 /api/v1 前缀, 此处只传相对路径
	// (曾误写 "/api/v1/profile" 致双前缀 404, 画像拉取静默失败, 2026-11-07 修复)
	if err := c.doJSON(ctx, c.meta, http.MethodGet, "/profile", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HistoryItem 云端图片历史条目(收藏恒置顶)。
type HistoryItem struct {
	JobID     string `json:"job_id"`
	CreatedAt string `json:"created_at"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Kind      string `json:"kind"`
	Fav       bool   `json:"fav"`
	ImageURL  string `json:"image_url"`
	Positive  string `json:"positive"`
	Seed      int64  `json:"seed"`
}

// GetHistory 云端图片历史(limit 内收藏置顶, 组内新→旧)。
func (c *Client) GetHistory(ctx context.Context) ([]HistoryItem, error) {
	return c.GetHistoryPage(ctx, 0, 100)
}

// GetHistoryPage 分页拉取云端历史(offset 起, 全部召回翻页用)。
func (c *Client) GetHistoryPage(ctx context.Context, offset, limit int) ([]HistoryItem, error) {
	return c.historyPage(ctx, offset, limit, false)
}

// historyPage 分页拉取历史; fav=true 时仅返回收藏(服务端 ?fav=1)。
func (c *Client) historyPage(ctx context.Context, offset, limit int, fav bool) ([]HistoryItem, error) {
	var out struct {
		Items []HistoryItem `json:"items"`
	}
	path := fmt.Sprintf("/history?limit=%d&offset=%d", limit, offset)
	if fav {
		path += "&fav=1"
	}
	// 历史响应可达几十KB(100条含提示词), meta 3s 在慢网络会超时 → 用 plan(60s)
	if err := c.doJSON(ctx, c.plan, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// GetFavJobIDs 拉取全部收藏任务ID(本地历史星标反查/只看收藏筛选用)。
func (c *Client) GetFavJobIDs(ctx context.Context) ([]string, error) {
	var ids []string
	for offset := 0; ; offset += 100 {
		items, err := c.historyPage(ctx, offset, 100, true)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			ids = append(ids, it.JobID)
		}
		if len(items) < 100 {
			return ids, nil
		}
	}
}

// HistoryFav 收藏/取消收藏。
func (c *Client) HistoryFav(ctx context.Context, jobID string, fav bool) error {
	return c.doJSON(ctx, c.plan, http.MethodPost, "/history/fav",
		map[string]any{"job_id": jobID, "fav": fav}, nil)
}

// DeviceRow 配对设备(设置页设备列表; 显示名由客户端决定: 手机=model, PC=device=机器名)。
type DeviceRow struct {
	Device       string `json:"device"`
	Type         string `json:"type"` // pc / android(旧空值按 pc 展示)
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	LastSeen     string `json:"last_seen"`
}

// ListDevices 拉取本身份已配对的全部设备(跨境慢网, 与 history 同用 plan 超时)。
func (c *Client) ListDevices(ctx context.Context) ([]DeviceRow, error) {
	var out struct {
		Devices []DeviceRow `json:"devices"`
	}
	if err := c.doJSON(ctx, c.plan, http.MethodGet, "/devices", nil, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// HistoryImage 下载历史图(服务端双模: OSS 302 或本地文件, doRaw 跟随重定向)。
func (c *Client) HistoryImage(ctx context.Context, jobID string) ([]byte, error) {
	return c.doRaw(ctx, c.long, http.MethodGet, "/history/"+url.PathEscape(jobID)+"/image")
}

// DeleteImage 删除云端图片副本(OSS 对象+服务端归档行); 失败由调用方降级只删本地(安卓同款)。
func (c *Client) DeleteImage(ctx context.Context, jobID string) error {
	return c.doJSON(ctx, c.plan, http.MethodPost, "/images/delete",
		map[string]any{"job_id": jobID}, nil)
}

// HistoryImageThumb 下载历史图缩略图(560宽 q80; 预览专用, 体积约为全图 1/25)。
func (c *Client) HistoryImageThumb(ctx context.Context, jobID string) ([]byte, error) {
	return c.doRaw(ctx, c.long, http.MethodGet, "/history/"+url.PathEscape(jobID)+"/image?thumb=1")
}

// allowedDownloadPrefixes 更新包下载的允许来源前缀(自有云端 + 官方软件源镜像)。
// Gitee/GitHub 直链需放行; 重定向后的真实域名不检查(以初始链接为准)。
var allowedDownloadPrefixes = []string{
	"https://gitee.com/",
	"https://github.com/",
	"https://raw.githubusercontent.com/",
}

// DownloadFile 从给定 URL 下载(仅允许自有云端与官方镜像来源, 保证安全边界)。
func (c *Client) DownloadFile(ctx context.Context, fileURL string) ([]byte, error) {
	return c.DownloadFileWithProgress(ctx, fileURL, nil)
}

// DownloadFileWithProgress 同 DownloadFile; progress 非 nil 时按块回调
// (已下载字节数, 总字节数; Content-Length 缺失时 total 为 -1)。
func (c *Client) DownloadFileWithProgress(ctx context.Context, fileURL string, progress func(got, total int64)) ([]byte, error) {
	allowed := c.BaseURL != "" && strings.HasPrefix(fileURL, c.BaseURL)
	for _, p := range allowedDownloadPrefixes {
		if strings.HasPrefix(fileURL, p) {
			allowed = true
			break
		}
	}
	if !allowed {
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
	if progress == nil {
		return io.ReadAll(resp.Body)
	}
	total := resp.ContentLength
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			progress(int64(len(buf)), total)
		}
		if rerr == io.EOF {
			return buf, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

// ---------- 设备扫码配对 ----------

// LinkCreateResp 旧设备生成一次性配对票据。
type LinkCreateResp struct {
	Code      string `json:"code"`
	URL       string `json:"url"` // 云端 H5 中转页(手机扫码打开)
	ExpiresAt string `json:"expires_at"`
}

// LinkCreate 旧设备创建配对票据(info 非空时顺带刷新本机设备属性)。
func (c *Client) LinkCreate(ctx context.Context, info *DeviceInfoPc) (*LinkCreateResp, error) {
	var out LinkCreateResp
	payload := map[string]any{"action": "create", "device": c.Device}
	if info != nil {
		payload["device_info"] = info
	}
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/devices/link", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkRedeem 新设备输入配对码领回身份(info 非空时登记本机厂商/型号)。
func (c *Client) LinkRedeem(ctx context.Context, code string, info *DeviceInfoPc) (*RegisterResp, error) {
	var out RegisterResp
	payload := map[string]any{"action": "redeem", "code": code, "device": c.Device}
	if info != nil {
		payload["device_info"] = info
	}
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/devices/link", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushProfile 保存画像后立即上云(与安卓 saveProfile 同一服务端接口, 双端行为一致:
// 服务端整体替换为本次全量, 未提及的键即删——服务端是唯一真相)。
// 返回新 profile_version; 失败返回 err, 调用方可静默(出图请求链路仍会兜底上行)。
func (c *Client) PushProfile(ctx context.Context, profile map[string]float64, disliked, custom, packs []string) (int, error) {
	var out struct {
		ProfileVersion int `json:"profile_version"`
	}
	payload := map[string]any{"profile": profile}
	if disliked != nil {
		payload["disliked"] = disliked
	}
	if custom != nil {
		payload["custom"] = custom
	}
	if packs != nil {
		payload["packs"] = packs
	}
	if err := c.doJSON(ctx, c.meta, http.MethodPost, "/profile", payload, &out); err != nil {
		return 0, err
	}
	return out.ProfileVersion, nil
}

// RefreshDevice 刷新本机设备属性(看设备列表前调, 存量行补报厂商/型号); 失败静默返回。
func (c *Client) RefreshDevice(ctx context.Context, info *DeviceInfoPc) {
	if info == nil {
		return
	}
	var out any
	_ = c.doJSON(ctx, c.meta, http.MethodPost, "/devices/refresh",
		map[string]any{"device": c.Device, "device_info": info}, &out)
}

// DeleteDevice 删除配对设备行(PC 端清理卸载重装产生的重复条目)。
func (c *Client) DeleteDevice(ctx context.Context, device string) error {
	var out any
	return c.doJSON(ctx, c.meta, http.MethodDelete, "/devices/"+url.PathEscape(device), nil, &out)
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
