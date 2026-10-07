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
	Width      int
	Height     int
	Success    bool
}

// AdminRecentPrompts 最近 n 条出图记录。
func (s *Store) AdminRecentPrompts(n int) ([]AdminPromptRow, error) {
	rows, err := s.db.Query(`SELECT at, user_id, COALESCE(source,''), COALESCE(workflow_id,''), COALESCE(positive,''), width, height, success
FROM prompt_records ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminPromptRow
	for rows.Next() {
		var r AdminPromptRow
		var success int
		if err := rows.Scan(&r.At, &r.UserID, &r.Source, &r.WorkflowID, &r.Positive, &r.Width, &r.Height, &success); err != nil {
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
	Day string
	PV  int64
	UV  int64
	DL  int64
}

// RecordSiteEvent 记录一次官网事件(kind: pv=页面浏览 / dl=下载; iph 为访客匿名哈希)。
func (s *Store) RecordSiteEvent(kind, iph string) error {
	_, err := s.db.Exec(`INSERT INTO site_events(ts, kind, iph) VALUES(?,?,?)`,
		time.Now().Format(timeFmt), kind, iph)
	return err
}

// SiteStats 官网统计: 总 PV / 总 UV / 总下载 + 近 n 天按天明细(日期倒序)。
func (s *Store) SiteStats(n int) (int64, int64, int64, []SiteDailyRow, error) {
	var pv, uv, dl int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind='pv'`).Scan(&pv); err != nil {
		return 0, 0, 0, nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT iph) FROM site_events WHERE kind='pv' AND iph<>''`).Scan(&uv); err != nil {
		return 0, 0, 0, nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM site_events WHERE kind='dl'`).Scan(&dl); err != nil {
		return 0, 0, 0, nil, err
	}
	since := time.Now().AddDate(0, 0, -n).Format(timeFmt)
	rows, err := s.db.Query(`SELECT substr(ts,1,10) AS d,
  COUNT(CASE WHEN kind='pv' THEN 1 END),
  COUNT(DISTINCT CASE WHEN kind='pv' AND iph<>'' THEN iph END),
  COUNT(CASE WHEN kind='dl' THEN 1 END)
FROM site_events WHERE ts >= ? GROUP BY d ORDER BY d DESC`, since)
	if err != nil {
		return pv, uv, dl, nil, err
	}
	defer rows.Close()
	var daily []SiteDailyRow
	for rows.Next() {
		var r SiteDailyRow
		if err := rows.Scan(&r.Day, &r.PV, &r.UV, &r.DL); err != nil {
			return pv, uv, dl, nil, err
		}
		daily = append(daily, r)
	}
	return pv, uv, dl, daily, rows.Err()
}
