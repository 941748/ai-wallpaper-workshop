package store

import "time"

// 管理看板查询(只读)。

// AdminPromptRow 看板用出图记录。
type AdminPromptRow struct {
	At         string
	UserID     string
	Source     string
	WorkflowID string
	Positive   string
	Negative   string
	Width      int
	Height     int
	Success    bool
}

// AdminRecentPrompts 最近 n 条出图记录。
func (s *Store) AdminRecentPrompts(n int) ([]AdminPromptRow, error) {
	rows, err := s.db.Query(`SELECT at, user_id, COALESCE(source,''), COALESCE(workflow_id,''), COALESCE(positive,''), COALESCE(negative,''), width, height, success
FROM prompt_records ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminPromptRow
	for rows.Next() {
		var r AdminPromptRow
		var success int
		if err := rows.Scan(&r.At, &r.UserID, &r.Source, &r.WorkflowID, &r.Positive, &r.Negative, &r.Width, &r.Height, &success); err != nil {
			return nil, err
		}
		r.Success = success != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminSignalRow 看板用漂移事件。
type AdminSignalRow struct {
	At     string
	UserID string
	Type   string
	Detail string
}

// AdminRecentSignals 最近 n 条漂移事件。
func (s *Store) AdminRecentSignals(n int) ([]AdminSignalRow, error) {
	rows, err := s.db.Query(`SELECT at, user_id, type, COALESCE(detail,'') FROM signals ORDER BY at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminSignalRow
	for rows.Next() {
		var r AdminSignalRow
		if err := rows.Scan(&r.At, &r.UserID, &r.Type, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminJobRow 看板用任务行。
type AdminJobRow struct {
	JobID    string
	Kind     string
	Priority string
	Status   string
	Error    string
}

// AdminRecentJobs 最近 n 条任务。
func (s *Store) AdminRecentJobs(n int) ([]AdminJobRow, error) {
	rows, err := s.db.Query(`SELECT job_id, kind, priority, status, COALESCE(error,'') FROM jobs ORDER BY created_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminJobRow
	for rows.Next() {
		var r AdminJobRow
		if err := rows.Scan(&r.JobID, &r.Kind, &r.Priority, &r.Status, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- 官网统计 ----------

// SiteDailyRow 官网某天的统计行。
type SiteDailyRow struct {
	Day   string
	PV    int64
	UV    int64
	DL    int64
	ImgDL int64
}

// RecordSiteEvent 记录一次官网事件(kind: pv=页面浏览 / dl=客户端下载 / img_dl=图片下载;
// iph 为访客匿名哈希或 user_id, ip 为访客来源明文 IP(供区域分析))。
func (s *Store) RecordSiteEvent(kind, iph, ip string) error {
	_, err := s.db.Exec(`INSERT INTO site_events(ts, kind, iph, ip) VALUES(?,?,?,?)`,
		time.Now().Format(timeFmt), kind, iph, ip)
	return err
}

// SiteStats 官网统计: 总 PV / 总 UV / 总客户端下载 / 总图片下载 + 近 n 天按天明细(日期倒序)。
func (s *Store) SiteStats(n int) (pv, uv, dl, imgDL int64, daily []SiteDailyRow, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind='pv'`).Scan(&pv); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(DISTINCT iph) FROM site_events WHERE kind='pv' AND iph<>''`).Scan(&uv); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind='dl'`).Scan(&dl); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind='img_dl'`).Scan(&imgDL); err != nil {
		return
	}
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, qErr := s.db.Query(`SELECT substr(ts,1,10) AS d,
  COUNT(CASE WHEN kind='pv' THEN 1 END),
  COUNT(DISTINCT CASE WHEN kind='pv' AND iph<>'' THEN iph END),
  COUNT(CASE WHEN kind='dl' THEN 1 END),
  COUNT(CASE WHEN kind='img_dl' THEN 1 END)
FROM site_events WHERE ts >= ? GROUP BY d ORDER BY d DESC`, since)
	if qErr != nil {
		err = qErr
		return
	}
	defer rows.Close()
	for rows.Next() {
		var r SiteDailyRow
		if err = rows.Scan(&r.Day, &r.PV, &r.UV, &r.DL, &r.ImgDL); err != nil {
			return
		}
		daily = append(daily, r)
	}
	err = rows.Err()
	return
}

// SiteEventRow 看板用官网事件行。
type SiteEventRow struct {
	TS   string
	Kind string
	IPh  string
	IP   string
}

// AdminRecentSiteEvents 最近 n 条官网事件(含 IP, 供区域分析)。
func (s *Store) AdminRecentSiteEvents(n int) ([]SiteEventRow, error) {
	rows, err := s.db.Query(`SELECT ts, kind, iph, ip FROM site_events ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteEventRow
	for rows.Next() {
		var r SiteEventRow
		if err := rows.Scan(&r.TS, &r.Kind, &r.IPh, &r.IP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- 出图统计 ----------

// GenDailyRow 某天的出图统计行。
type GenDailyRow struct {
	Day     string
	Total   int64
	Success int64
}

// GenStats 出图统计: 总出图 / 成功出图 + 近 n 天按天明细(日期倒序)。
func (s *Store) GenStats(n int) (total, success int64, daily []GenDailyRow, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM prompt_records`).Scan(&total); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM prompt_records WHERE success=1`).Scan(&success); err != nil {
		return
	}
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, qErr := s.db.Query(`SELECT substr(at,1,10) AS d,
  COUNT(*),
  COUNT(CASE WHEN success=1 THEN 1 END)
FROM prompt_records WHERE at >= ? GROUP BY d ORDER BY d DESC`, since)
	if qErr != nil {
		err = qErr
		return
	}
	defer rows.Close()
	for rows.Next() {
		var r GenDailyRow
		if err = rows.Scan(&r.Day, &r.Total, &r.Success); err != nil {
			return
		}
		daily = append(daily, r)
	}
	err = rows.Err()
	return
}

// ---------- P0 看板改版(2026-11-06): 分页签 + 统计图表 ----------

// GrowthRow 某日新增用户行。
type GrowthRow struct {
	Day string
	New int64
}

// UserGrowthDaily 近 n 天每日新增用户(日期升序)。
func (s *Store) UserGrowthDaily(n int) ([]GrowthRow, error) {
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT substr(created_at,1,10) AS d, COUNT(*)
FROM users WHERE created_at >= ? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GrowthRow
	for rows.Next() {
		var r GrowthRow
		if err := rows.Scan(&r.Day, &r.New); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserPrefRow 看板用户行(含画像原料, 供偏好列与聚合分析)。
type UserPrefRow struct {
	UserID       string
	CreatedAt    string
	Disabled     bool
	PromptCount  int64
	LastPromptAt string
	Profile      string // JSON map[string]float64, ""=无画像
	Packs        string // JSON []string, ""=旧档未设置, "[]"=显式清空
	Disliked     string // JSON []string
	ClientVer    string // 最近上报的客户端版本, ""=尚未上报
}

// AdminUserPrefRows 全部用户 LEFT JOIN 画像快照 + 出图数/最近出图(最近出图倒序, 无出图按注册倒序)。
func (s *Store) AdminUserPrefRows() ([]UserPrefRow, error) {
	rows, err := s.db.Query(`SELECT u.user_id, u.created_at, u.disabled,
  COALESCE((SELECT COUNT(*) FROM prompt_records p WHERE p.user_id = u.user_id), 0),
  COALESCE((SELECT MAX(at) FROM prompt_records p WHERE p.user_id = u.user_id), '') AS last_at,
  COALESCE(pr.profile, ''), COALESCE(pr.packs, ''), COALESCE(pr.disliked, ''), u.client_ver
FROM users u LEFT JOIN user_profiles pr ON pr.user_id = u.user_id
ORDER BY last_at DESC, u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserPrefRow
	for rows.Next() {
		var r UserPrefRow
		var disabled int
		if err := rows.Scan(&r.UserID, &r.CreatedAt, &disabled, &r.PromptCount,
			&r.LastPromptAt, &r.Profile, &r.Packs, &r.Disliked, &r.ClientVer); err != nil {
			return nil, err
		}
		r.Disabled = disabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClientVerRow 客户端版本分布行。
type ClientVerRow struct {
	Ver    string
	N      int64
	LastAt string
}

// ClientVerDist 各客户端版本的用户数与最近上报时间。
func (s *Store) ClientVerDist() ([]ClientVerRow, error) {
	rows, err := s.db.Query(`SELECT client_ver, COUNT(*), COALESCE(MAX(client_ver_at),'')
FROM users WHERE client_ver <> '' GROUP BY client_ver`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientVerRow
	for rows.Next() {
		var r ClientVerRow
		if err := rows.Scan(&r.Ver, &r.N, &r.LastAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GenSourceRow 某日某来源的出图行。
type GenSourceRow struct {
	Day     string
	Source  string
	Total   int64
	Success int64
}

// GenDailyBySource 近 n 天按天×来源出图统计(日期升序; 空来源归 unknown)。
func (s *Store) GenDailyBySource(n int) ([]GenSourceRow, error) {
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT substr(at,1,10) AS d, COALESCE(NULLIF(source,''),'unknown') AS src,
  COUNT(*), COALESCE(SUM(success),0)
FROM prompt_records WHERE at >= ? GROUP BY d, src ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenSourceRow
	for rows.Next() {
		var r GenSourceRow
		if err := rows.Scan(&r.Day, &r.Source, &r.Total, &r.Success); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResRow 分辨率统计行。
type ResRow struct {
	W, H int
	N    int64
}

// ResolutionTop 近 n 天成功出图的分辨率 Top(按量倒序)。
func (s *Store) ResolutionTop(n, limit int) ([]ResRow, error) {
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT width, height, COUNT(*) AS n FROM prompt_records
WHERE at >= ? AND success = 1 AND width > 0 AND height > 0
GROUP BY width, height ORDER BY n DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResRow
	for rows.Next() {
		var r ResRow
		if err := rows.Scan(&r.W, &r.H, &r.N); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ComboCountRow combo 频次行(combo JSON 原文 + 次数)。
type ComboCountRow struct {
	Combo string
	N     int64
}

// ComboFreq 近 n 天成功出图的 combo 频次(按维聚合在调用侧做)。
func (s *Store) ComboFreq(n int) ([]ComboCountRow, error) {
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT combo, COUNT(*) AS n FROM prompt_records
WHERE at >= ? AND success = 1 AND combo <> '' GROUP BY combo`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComboCountRow
	for rows.Next() {
		var r ComboCountRow
		if err := rows.Scan(&r.Combo, &r.N); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageDayRow 某日用量行。
type UsageDayRow struct {
	Day string
	LLM int64
	Gen int64
}

// UsageDaily 近 n 天用量按天(日期升序)。
func (s *Store) UsageDaily(n int) ([]UsageDayRow, error) {
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT substr(hour,1,10) AS d, COALESCE(SUM(llm_count),0), COALESCE(SUM(gen_count),0)
FROM usage_counters WHERE hour >= ? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDayRow
	for rows.Next() {
		var r UsageDayRow
		if err := rows.Scan(&r.Day, &r.LLM, &r.Gen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PregenStats 预生成池统计(总数 / 就绪数)。
func (s *Store) PregenStats() (total, ready int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ready),0) FROM pregen`).Scan(&total, &ready)
	return
}

// CurrentSchema 当前迁移版本号(导出给看板)。
func (s *Store) CurrentSchema() (int, error) {
	return s.schemaVersion()
}

// AdminDeviceRow 设备行。
type AdminDeviceRow struct {
	Device, UserID, CreatedAt          string
	Type, Model, OS, OSVersion, AppVer string
	ScreenW, ScreenH                   int
}

// AdminDeviceRows 最近 n 条设备记录(注册/配对上报)。
func (s *Store) AdminDeviceRows(n int) ([]AdminDeviceRow, error) {
	rows, err := s.db.Query(`SELECT device, user_id, created_at, device_type, model, os, os_version,
  app_version, screen_w, screen_h FROM devices
ORDER BY COALESCE(NULLIF(last_seen,''), created_at) DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminDeviceRow
	for rows.Next() {
		var r AdminDeviceRow
		if err := rows.Scan(&r.Device, &r.UserID, &r.CreatedAt, &r.Type, &r.Model, &r.OS,
			&r.OSVersion, &r.AppVer, &r.ScreenW, &r.ScreenH); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SiteEventCount 某类官网事件总数(如 dl / apk_dl)。
func (s *Store) SiteEventCount(kind string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind=?`, kind).Scan(&n)
	return n, err
}

// HasPCDevice 用户是否登记过 PC 设备(= 已与电脑端配对)。
func (s *Store) HasPCDevice(userID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE user_id=? AND device_type='pc'`, userID).Scan(&n)
	return n > 0, err
}
