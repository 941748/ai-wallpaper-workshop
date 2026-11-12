package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"


	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/install"
	"wallpaper/internal/policy"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/tick"
	"wallpaper/internal/update"
)

// ---------- 当前壁纸页 ----------

// CurrentInfo 当前壁纸状态。
type CurrentInfo struct {
	Exists bool   `json:"exists"`
	Image  string `json:"image,omitempty"` // JPEG base64 预览
	Path   string `json:"path,omitempty"`
	Hint   string `json:"hint"` // "上次调整偏好: N 天前" 等
}

// GetCurrent 当前壁纸大图 + 偏好调整提示。
func (b *Bridge) GetCurrent() CurrentInfo {
	b.mu.Lock()
	dir := b.dir
	st := b.st
	b.mu.Unlock()
	out := CurrentInfo{Hint: ""}
	if p, err := config.LoadProfile(dir); err == nil && !p.UpdatedAt.IsZero() {
		days := int(time.Since(p.UpdatedAt).Hours() / 24)
		if days <= 0 {
			out.Hint = "上次调整偏好: 今天 — 想微调可到\"偏好\"页"
		} else {
			out.Hint = fmt.Sprintf("上次调整偏好: %d 天前 — 想微调可到\"偏好\"页", days)
		}
	}
	path := st.CurrentPath()
	if path == "" {
		return out
	}
	b64, err := readFileB64(path, 560, 315)
	if err != nil {
		return out
	}
	out.Exists = true
	out.Image = b64
	out.Path = path
	return out
}

// RateCurrent 评价当前壁纸(喜欢/不喜欢, 附五维组合供云端针对性调整)。
func (b *Bridge) RateCurrent(typ string) (string, error) {
	b.mu.Lock()
	dir, st := b.dir, b.st
	b.mu.Unlock()
	path := st.CurrentPath()
	if path == "" {
		return "", fmt.Errorf("当前没有可评价的壁纸")
	}
	word := "不喜欢"
	if typ == signals.TypeLiked {
		word = "喜欢"
	}
	ev := signals.Event{Type: typ, Detail: filepath.Base(path)}
	if h, err := st.LoadHistory(); err == nil && len(h) > 0 {
		if combo := h[len(h)-1].Combo; len(combo) > 0 {
			ev.Extra = map[string]any{"combo": combo}
		}
	}
	if err := signals.Append(dir, ev); err != nil {
		return "", fmt.Errorf("%s", friendlyErrText(err))
	}
	if typ == signals.TypeLiked {
		return "已记录 " + word + " — 之后会多给你这类画面 ✓", nil
	}
	return "已记录 " + word + " — 之后会避开这类画面 ✓", nil
}

// SwapResult 立即换一张结果。
type SwapResult struct {
	Started bool   `json:"started"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
	Image   string `json:"image,omitempty"`
}

// SwapNow 立即换一张: 拉起隐藏 --tick --force 并等结果(最长 15 分钟)。
// 阻塞调用; 前端 Promise 天然异步。进度阶段经 swap-progress 事件推送(与安卓分段提示一致)。
func (b *Bridge) SwapNow() SwapResult {
	b.mu.Lock()
	dir := b.dir
	b.mu.Unlock()
	_ = os.Remove(tick.OutcomePath(dir))
	_ = os.Remove(tick.ProgressPath(dir))
	exe, err := os.Executable()
	if err != nil {
		return SwapResult{Message: "触发失败: " + friendlyText(err.Error())}
	}
	cmd, err := scheduler.RunTickNow(exe)
	if err != nil {
		return SwapResult{Message: "触发失败: " + friendlyText(err.Error())}
	}
	waitCh := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitCh) }()

	var res *tick.Outcome
	lastStage := ""
	syncProgress := func() {
		if s, err := tick.ReadProgress(dir); err == nil && s != "" && s != lastStage {
			lastStage = s
			b.emit("swap-progress", map[string]any{"stage": s})
		}
	}
	timeout := time.After(15 * time.Minute)
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for res == nil {
		select {
		case <-waitCh:
			syncProgress()
			if o, err := tick.ReadOutcome(dir); err == nil {
				res = &o
			} else {
				res = &tick.Outcome{Detail: "换图进程异常退出(未写结果)"}
			}
		case <-poll.C:
			syncProgress()
			if o, err := tick.ReadOutcome(dir); err == nil {
				res = &o
			}
		case <-timeout:
			res = &tick.Outcome{Detail: "仍在出图中(超过 15 分钟), 可稍后查看桌面"}
		}
	}
	_ = os.Remove(tick.ProgressPath(dir))
	if !res.Changed {
		return SwapResult{Changed: false, Message: "未换图: " + friendlyText(res.Detail)}
	}
	out := SwapResult{Changed: true, Message: "✓ 已换壁纸 — 新图已生效"}
	if cur := b.st.CurrentPath(); cur != "" {
		if b64, err := readFileB64(cur, 560, 315); err == nil {
			out.Image = b64
		}
	}
	return out
}

// ---------- 设置页: 服务与调度 ----------

// ServiceState 服务/换图/回访表单状态。
type ServiceState struct {
	Interval    int      `json:"interval"`
	AutoChange  bool     `json:"autoChange"`
	PauseHours  int      `json:"pauseHours"`
	PauseOpts   []int    `json:"pauseOpts"`
	PauseHint   string   `json:"pauseHint"`
	AskEnabled  bool     `json:"askEnabled"`
	AskDays     int      `json:"askDays"`
	TaskState   string   `json:"taskState"`
	Version     string   `json:"version"`
	LastUpdate  string   `json:"lastUpdate"`
	UserID      string   `json:"userId"`
	SyncState   string   `json:"syncState"`
}

// GetServiceState 读取设置页表单状态。
func (b *Bridge) GetServiceState() ServiceState {
	b.mu.Lock()
	cfg := *b.cfg
	dir := b.dir
	b.mu.Unlock()
	out := ServiceState{
		Interval:    cfg.IntervalHours,
		AutoChange:  cfg.AutoChange,
		PauseHours:  cfg.PauseHours,
		PauseOpts:   policy.Load(dir).PauseOptions,
		AskEnabled:  cfg.AskEnabled,
		AskDays:     cfg.Satisfaction.IntervalDays,
		Version:     update.CurrentVersion,
		UserID:      cfg.UserID,
		SyncState:   "正常(每轮出图自动同步)",
	}
	if cfg.LastUpdate == "" {
		out.LastUpdate = "安装后自动记录"
	} else if t, err := time.Parse(time.RFC3339, cfg.LastUpdate); err == nil {
		out.LastUpdate = t.Local().Format("2006-01-02 15:04")
	} else {
		out.LastUpdate = cfg.LastUpdate
	}
	// 暂停提示
	if !cfg.AutoChange {
		if t, err := time.Parse(time.RFC3339, cfg.PauseUntil); err == nil && time.Now().Before(t) {
			out.PauseHint = fmt.Sprintf("已暂停, 将于 %s 自动恢复", t.Format("01-02 15:04"))
		} else {
			out.PauseHint = "保存后按所选时长暂停, 到期自动恢复"
		}
	}
	// 计划任务状态(异步查询, 这里同步拿, 失败显示检测中由前端再调 RepairTask 刷新)
	if state, err := scheduler.QueryState(); err == nil && state != "" {
		out.TaskState = state
	} else {
		out.TaskState = "未检测到(可能被杀软拦截)"
	}
	return out
}

// SaveServiceReq 保存设置请求。
type SaveServiceReq struct {
	Interval    int    `json:"interval"`
	AutoChange  bool   `json:"autoChange"`
	PauseHours  int    `json:"pauseHours"`
	AskEnabled  bool   `json:"askEnabled"`
	AskDays     int    `json:"askDays"`
}

// SaveService 保存服务/调度表单(含计划任务幂等重注册)。
func (b *Bridge) SaveService(req SaveServiceReq) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cfg := b.cfg
	if req.Interval > 0 {
		cfg.IntervalHours = req.Interval
	}
	cfg.AskEnabled = req.AskEnabled
	cfg.AutoChange = req.AutoChange
	if cfg.AutoChange {
		cfg.PauseUntil = ""
		cfg.PauseHours = 0
	} else {
		cfg.PauseHours = req.PauseHours
		cfg.PauseUntil = time.Now().Add(time.Duration(req.PauseHours) * time.Hour).Format(time.RFC3339)
	}
	if days := req.AskDays; days >= 1 && days != cfg.Satisfaction.IntervalDays {
		cfg.Satisfaction.IntervalDays = days
		cfg.Satisfaction.NextAskAt = time.Now().AddDate(0, 0, days).Format(time.RFC3339)
	}
	if err := cfg.Save(b.dir); err != nil {
		return fmt.Errorf("保存失败: %s", friendlyErrText(err))
	}
	b.be, b.cc = makeBackend(cfg)
	if exe, err := os.Executable(); err == nil {
		if _, err := install.Setup(b.st.BinDir(), exe, update.CurrentVersion, cfg.PhaseMinutes); err != nil {
			b.st.Log("settings: 安装/任务注册失败: %v", err)
		} else {
			cfg.TaskTickHours = scheduler.TickHours
			_ = cfg.Save(b.dir)
		}
	}
	return nil
}

// RepairTask 重新注册计划任务并校验(便携/开发模式直接提示无需修复)。
func (b *Bridge) RepairTask() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法获取程序路径: %s", friendlyErrText(err))
	}
	// installed 守卫: 仅 bin\ 目录下的程序才写系统级副作用(防 go test 劫持, 见记忆)
	if filepath.Base(filepath.Dir(exe)) != "bin" {
		return "当前为便携/开发模式, 未安装计划任务, 无需修复。", nil
	}
	b.mu.Lock()
	phase := b.cfg.PhaseMinutes
	b.mu.Unlock()
	if err := scheduler.Register(exe, scheduler.TickHours, phase); err != nil {
		b.st.Log("settings: 计划任务修复失败: %v", err)
		return "", fmt.Errorf("计划任务修复失败, 未能确认注册成功: %s\n多半是被 360安全等杀毒软件拦截。请暂时关闭杀软的拦截防护, 再重试", friendlyErrText(err))
	}
	state, err := scheduler.VerifyRegistered(exe)
	if err != nil {
		return "", fmt.Errorf("计划任务修复失败, 未能确认注册成功: %s", friendlyErrText(err))
	}
	b.st.Log("settings: 计划任务修复成功, 状态=%s", state)
	return "计划任务已修复 ✓ (状态: " + state + ")", nil
}

// ---------- 软件更新 ----------

// UpdateResult 手动检查更新结果。
type UpdateResult struct {
	Status  string `json:"status"` // "latest" | "available" | "error"
	Version string `json:"version,omitempty"`
	Message string `json:"message"`
}

// CheckUpdate 查 manifest 判断是否有新版本(不下载)。
func (b *Bridge) CheckUpdate() UpdateResult {
	b.mu.Lock()
	cfg := *b.cfg
	b.mu.Unlock()
	c := cloudNew(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	latest, err := c.ClientLatest(ctx, update.CurrentVersion)
	if err != nil {
		b.st.Log("手动更新: 查询更新失败: %v", err)
		return UpdateResult{Status: "error", Message: "查询更新失败: " + friendlyErrText(err) + ", 请稍后重试"}
	}
	if latest.Version == "" || update.CompareVersions(latest.Version, update.CurrentVersion) <= 0 {
		return UpdateResult{Status: "latest", Message: "当前已是最新版本 (" + update.CurrentVersion + ")"}
	}
	return UpdateResult{Status: "available", Version: latest.Version,
		Message: fmt.Sprintf("发现新版本 %s (当前 %s)", latest.Version, update.CurrentVersion)}
}

// DoUpdate 下载新版本并预启动换装助手; 返回后前端关闭窗口即完成换装。
func (b *Bridge) DoUpdate() UpdateResult {
	b.mu.Lock()
	dir := b.dir
	cfg := *b.cfg
	b.mu.Unlock()
	exe, err := os.Executable()
	if err != nil {
		return UpdateResult{Status: "error", Message: "无法定位程序路径: " + friendlyErrText(err)}
	}
	c := cloudNew(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	latest, err := c.ClientLatest(ctx, update.CurrentVersion)
	cancel()
	if err != nil {
		return UpdateResult{Status: "error", Message: "查询更新失败: " + friendlyErrText(err) + ", 请稍后重试"}
	}
	if latest.Version == "" || update.CompareVersions(latest.Version, update.CurrentVersion) <= 0 {
		return UpdateResult{Status: "latest", Message: "当前已是最新版本 (" + update.CurrentVersion + ")"}
	}
	b.st.Log("手动更新: 用户确认下载 %s (当前 %s)", latest.Version, update.CurrentVersion)
	// 进度经事件推送(200ms 节流)
	var lastEmit time.Time
	progress := func(got, total int64) {
		now := time.Now()
		if now.Sub(lastEmit) < 200*time.Millisecond && (total <= 0 || got < total) {
			return
		}
		lastEmit = now
		b.emit("update-progress", map[string]any{"got": got, "total": total})
	}
	dlCtx, dlCancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer dlCancel()
	if !update.Check(dlCtx, c, dir, exe, progress, b.st.Log) {
		b.st.Log("手动更新: 下载或校验未通过")
		return UpdateResult{Status: "error", Message: "更新失败: 下载或校验未通过, 详见日志(数据目录 logs)。"}
	}
	b.st.Log("手动更新: 已暂存新版本 %s, 关闭窗口换装", latest.Version)
	return UpdateResult{Status: "available", Version: latest.Version,
		Message: "更新完成, 正在关闭…"}
}

// ---------- 设备同步(配对码) ----------

// LinkResult 配对码生成/兑换结果。
type LinkResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	URL     string `json:"url,omitempty"`
	UserID  string `json:"userId,omitempty"`
	Message string `json:"message"`
}

// CreateLink 生成配对码(多设备一致性入口: 换机/重装/手机↔PC 均用它领回身份)。
func (b *Bridge) CreateLink() LinkResult {
	b.mu.Lock()
	cc := b.cc
	b.mu.Unlock()
	if cc == nil {
		return LinkResult{Message: "当前为直连调试模式, 无云端同步能力。"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := cc.LinkCreate(ctx, pcDeviceInfo())
	if err != nil {
		return LinkResult{Message: "生成失败: " + friendlyText(err.Error())}
	}
	out := LinkResult{OK: true, Code: resp.Code, URL: resp.URL,
		Message: fmt.Sprintf("配对码 %s 已生成(5 分钟内有效)", resp.Code)}
	return out
}

// RedeemLink 输入配对码领回身份( adopt 生成方 user_id, 偏好与云端图片随身份走)。
func (b *Bridge) RedeemLink(code string) LinkResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cc == nil {
		return LinkResult{Message: "当前为直连调试模式, 无云端同步能力。"}
	}
	code = trimSpace(code)
	if code == "" {
		return LinkResult{Message: "请输入配对码"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := b.cc.LinkRedeem(ctx, code, pcDeviceInfo())
	if err != nil {
		return LinkResult{Message: "配对失败: " + friendlyText(err.Error())}
	}
	b.cfg.UserID = resp.UserID
	b.cfg.Token = resp.Token
	_ = b.cfg.Save(b.dir)
	_ = signals.Append(b.dir, signals.Event{Type: signals.TypeDeviceLinked, Detail: "设备配对成功(多设备同步)"})
	b.be, b.cc = makeBackend(b.cfg)
	return LinkResult{OK: true, UserID: resp.UserID, Message: "配对成功 ✓ 偏好与云端图片已同步"}
}

// PairedDevice 配对设备显示行(设置页设备同步区)。
type PairedDevice struct {
	Device   string `json:"device"`
	Type     string `json:"type"`   // pc / android(旧空值按 pc)
	Name     string `json:"name"`   // 显示名: 手机=型号(model), PC=机器名(device 列)
	Current  bool   `json:"current"` // 本机
	LastSeen string `json:"lastSeen"`
}

// GetPairedDevices 已配对设备列表: 配对几台返回几台, 手机显示型号、PC 显示机器名。
func (b *Bridge) GetPairedDevices() ([]PairedDevice, error) {
	b.mu.Lock()
	cc := b.cc
	b.mu.Unlock()
	if cc == nil {
		return nil, fmt.Errorf("当前为直连调试模式, 无云端同步能力")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cc.RefreshDevice(ctx, pcDeviceInfo()) // 存量行补报厂商/型号(失败静默, 不阻塞列表)
	rows, err := cc.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("设备列表获取失败: %s", friendlyText(err.Error()))
	}
	out := make([]PairedDevice, 0, len(rows))
	for _, r := range rows {
		hw := joinNonEmpty(r.Manufacturer, r.Model)
		var name string
		if r.Type == "android" {
			// 安卓: 厂商+型号(如 华为 VOG-AL10); 旧数据无属性则退回 device
			name = hw
			if name == "" {
				name = r.Device
			}
		} else if hw == "" {
			name = r.Device
		} else {
			// PC(负责人定方案A): 厂商+型号（机器名）
			name = hw + "（" + r.Device + "）"
		}
		out = append(out, PairedDevice{
			Device: r.Device, Type: r.Type, Name: name,
			Current: r.Device == cc.Device, LastSeen: r.LastSeen,
		})
	}
	return out, nil
}

// joinNonEmpty 拼接非空片段(空格分隔)。
func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// HasPairData 本机是否已有本地数据(壁纸留档或画像文件)——「加入对方」前的数据迁移警示用。
func (b *Bridge) HasPairData() bool {
	b.mu.Lock()
	dir := b.dir
	b.mu.Unlock()
	if ws, err := filepath.Glob(filepath.Join(dir, "wallpapers", "*")); err == nil && len(ws) > 0 {
		return true
	}
	if fi, err := os.Stat(filepath.Join(dir, "profile.json")); err == nil && fi.Size() > 100 {
		return true
	}
	return false
}

// DeletePairedDevice 删除配对设备行(PC 端清理卸载重装产生的重复条目; 负责人定: 安卓不提供删除)。
func (b *Bridge) DeletePairedDevice(device string) error {
	b.mu.Lock()
	cc := b.cc
	b.mu.Unlock()
	if cc == nil {
		return fmt.Errorf("当前为直连调试模式, 无云端同步能力")
	}
	if device == "" {
		return fmt.Errorf("缺少设备标识")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cc.DeleteDevice(ctx, device); err != nil {
		return fmt.Errorf("删除失败: %s", friendlyText(err.Error()))
	}
	return nil
}

// ---------- 卸载 ----------

// Uninstall 执行卸载(保留或删除数据), 之后进程退出。
func (b *Bridge) Uninstall(removeData bool) {
	b.mu.Lock()
	dir := b.dir
	b.mu.Unlock()
	binPath := install.BinPath(filepath.Join(dir, "bin"))
	_ = install.Uninstall(binPath, dir, removeData)
	os.Exit(0)
}

// ---------- 小工具 ----------

// cloudNew 构造云客户端(更新/配对用)。
func cloudNew(cfg config.Config) *cloud.Client {
	return cloud.New(cfg.CloudURL, cfg.UserID, cfg.Token, hostName())
}

// ReleaseNotesURL 更新内容页(Gitee Releases, 国内可达)。
const ReleaseNotesURL = "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases"
