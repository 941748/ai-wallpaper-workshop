package store

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
