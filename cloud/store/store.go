// Package store 云服务 SQLite 存储(首版单文件, 量大再迁 PostgreSQL)。
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store 云服务存储句柄。
type Store struct {
	db *sql.DB
}

// Open 打开(并初始化)数据库。
func Open(path string) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS users (
  user_id    TEXT PRIMARY KEY,
  token      TEXT NOT NULL,
  created_at TEXT NOT NULL,
  disabled   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS devices (
  device     TEXT,
  user_id    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS prompt_records (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id         TEXT NOT NULL,
  at              TEXT NOT NULL,
  positive        TEXT,
  negative        TEXT,
  combo           TEXT,
  seed            INTEGER,
  width           INTEGER,
  height          INTEGER,
  workflow_id     TEXT,
  source          TEXT,
  duration_ms     INTEGER,
  success         INTEGER,
  profile_version INTEGER
);
CREATE INDEX IF NOT EXISTS idx_prompt_user ON prompt_records(user_id, at);
CREATE TABLE IF NOT EXISTS signals (
  id      TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  at      TEXT NOT NULL,
  type    TEXT NOT NULL,
  detail  TEXT,
  extra   TEXT
);
CREATE INDEX IF NOT EXISTS idx_sig_user ON signals(user_id, at);
CREATE TABLE IF NOT EXISTS usage_counters (
  user_id   TEXT NOT NULL,
  hour      TEXT NOT NULL,
  llm_count INTEGER NOT NULL DEFAULT 0,
  gen_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, hour)
);
CREATE TABLE IF NOT EXISTS jobs (
  job_id      TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  kind        TEXT NOT NULL DEFAULT 'gen',
  priority    TEXT NOT NULL DEFAULT 'normal',
  status      TEXT NOT NULL DEFAULT 'queued',
  error       TEXT,
  positive    TEXT,
  negative    TEXT,
  combo       TEXT,
  width       INTEGER,
  height      INTEGER,
  seed        INTEGER,
  workflow_id TEXT,
  image_path  TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_queue ON jobs(status, priority, created_at);
CREATE TABLE IF NOT EXISTS pregen (
  user_id         TEXT PRIMARY KEY,
  profile_version INTEGER NOT NULL,
  job_id          TEXT,
  ready           INTEGER NOT NULL DEFAULT 0,
  positive        TEXT,
  negative        TEXT,
  combo           TEXT,
  seed            INTEGER,
  workflow_id     TEXT,
  width           INTEGER,
  height          INTEGER,
  image_path      TEXT,
  updated_at      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS link_tickets (
  code       TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS drift_reports (
  user_id    TEXT PRIMARY KEY,
  summary    TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS site_events (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  ts   TEXT NOT NULL,
  kind TEXT NOT NULL,
  iph  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_site_events ON site_events(kind, ts);
`
	_, err := s.db.Exec(schema)
	return err
}

const timeFmt = time.RFC3339

// ---------- users / devices ----------

// CreateUser 创建匿名用户。
func (s *Store) CreateUser(userID, token string) error {
	_, err := s.db.Exec(`INSERT INTO users(user_id, token, created_at) VALUES(?,?,?)`,
		userID, token, time.Now().Format(timeFmt))
	return err
}

// User 用户信息。
type User struct {
	UserID    string
	Token     string
	CreatedAt string
	Disabled  bool
}

// UserByToken 按令牌查询用户。
func (s *Store) UserByToken(token string) (*User, error) {
	return s.queryUser(`SELECT user_id, token, created_at, disabled FROM users WHERE token=?`, token)
}

// UserByID 按 ID 查询用户。
func (s *Store) UserByID(userID string) (*User, error) {
	return s.queryUser(`SELECT user_id, token, created_at, disabled FROM users WHERE user_id=?`, userID)
}

func (s *Store) queryUser(q string, arg any) (*User, error) {
	row := s.db.QueryRow(q, arg)
	var u User
	var disabled int
	if err := row.Scan(&u.UserID, &u.Token, &u.CreatedAt, &disabled); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Disabled = disabled != 0
	return &u, nil
}

// AddDevice 记录设备。
func (s *Store) AddDevice(device, userID string) error {
	_, err := s.db.Exec(`INSERT INTO devices(device, user_id, created_at) VALUES(?,?,?)`,
		device, userID, time.Now().Format(timeFmt))
	return err
}

// ---------- prompt records ----------

// PromptRecord 一轮出图记录。
type PromptRecord struct {
	At         time.Time
	Positive   string
	Negative   string
	Combo      string
	Seed       int64
	Width      int
	Height     int
	WorkflowID string
	Source     string
	DurationMs int64
	Success    bool
	ProfileVer int
}

// InsertPrompt 存一条记录。
func (s *Store) InsertPrompt(userID string, r PromptRecord) error {
	_, err := s.db.Exec(`INSERT INTO prompt_records
(user_id, at, positive, negative, combo, seed, width, height, workflow_id, source, duration_ms, success, profile_version)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		userID, r.At.Format(timeFmt), r.Positive, r.Negative, r.Combo, r.Seed,
		r.Width, r.Height, r.WorkflowID, r.Source, r.DurationMs, b2i(r.Success), r.ProfileVer)
	return err
}

// RecentPrompts 最近 n 条记录(供 LLM 参考)。
func (s *Store) RecentPrompts(userID string, n int) ([]PromptRecord, error) {
	rows, err := s.db.Query(`SELECT at, positive, negative, combo, seed, width, height, workflow_id, source, duration_ms, success, profile_version
FROM prompt_records WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PromptRecord
	for rows.Next() {
		var r PromptRecord
		var at string
		var success int
		if err := rows.Scan(&at, &r.Positive, &r.Negative, &r.Combo, &r.Seed, &r.Width, &r.Height,
			&r.WorkflowID, &r.Source, &r.DurationMs, &success, &r.ProfileVer); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(timeFmt, at)
		r.Success = success != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- signals ----------

// Signal 漂移事件。
type Signal struct {
	ID     string
	At     time.Time
	Type   string
	Detail string
	Extra  string
}

// InsertSignals 批量存事件(按 ID 去重)。
func (s *Store) InsertSignals(userID string, evs []Signal) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range evs {
		_, err := tx.Exec(`INSERT OR IGNORE INTO signals(id, user_id, at, type, detail, extra) VALUES(?,?,?,?,?,?)`,
			e.ID, userID, e.At.Format(timeFmt), e.Type, e.Detail, e.Extra)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecentSignals 一段时间内的最近事件。
func (s *Store) RecentSignals(userID string, since time.Time, n int) ([]Signal, error) {
	rows, err := s.db.Query(`SELECT id, at, type, detail FROM signals
WHERE user_id=? AND at>=? ORDER BY at DESC LIMIT ?`, userID, since.Format(timeFmt), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signal
	for rows.Next() {
		var e Signal
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Type, &e.Detail); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(timeFmt, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountSignals 统计某类事件(用于漂移摘要)。
func (s *Store) CountSignals(userID, typ string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM signals WHERE user_id=? AND type=? AND at>=?`,
		userID, typ, since.Format(timeFmt)).Scan(&n)
	return n, err
}

// ---------- usage ----------

// BumpUsage 计数(hour 桶; kind: llm | gen)。
func (s *Store) BumpUsage(userID, kind string) error {
	hour := time.Now().Format("2006-01-02T15")
	col := "llm_count"
	if kind == "gen" {
		col = "gen_count"
	}
	_, err := s.db.Exec(fmt.Sprintf(`INSERT INTO usage_counters(user_id, hour, %s) VALUES(?,?,1)
ON CONFLICT(user_id, hour) DO UPDATE SET %s = %s + 1`, col, col, col), userID, hour)
	return err
}

// UsageToday 今日用量。
func (s *Store) UsageToday(userID string) (llm, gen int, err error) {
	day := time.Now().Format("2006-01-02")
	err = s.db.QueryRow(`SELECT COALESCE(SUM(llm_count),0), COALESCE(SUM(gen_count),0) FROM usage_counters
WHERE user_id=? AND hour LIKE ?`, userID, day+"%").Scan(&llm, &gen)
	return
}

// UsageHour 本小时用量。
func (s *Store) UsageHour(userID string) (llm, gen int, err error) {
	hour := time.Now().Format("2006-01-02T15")
	err = s.db.QueryRow(`SELECT COALESCE(llm_count,0), COALESCE(gen_count,0) FROM usage_counters
WHERE user_id=? AND hour=?`, userID, hour).Scan(&llm, &gen)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return
}

// ---------- jobs ----------

// Job 出图任务。
type Job struct {
	JobID      string
	UserID     string
	Kind       string // gen | pregen | probe
	Priority   string // normal | idle
	Status     string // queued | running | done | failed
	Error      string
	Positive   string
	Negative   string
	Combo      string
	Width      int
	Height     int
	Seed       int64
	WorkflowID string
	ImagePath  string
}

// CreateJob 建任务。
func (s *Store) CreateJob(j Job) error {
	now := time.Now().Format(timeFmt)
	_, err := s.db.Exec(`INSERT INTO jobs(job_id, user_id, kind, priority, status, positive, negative, combo, width, height, seed, workflow_id, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.JobID, j.UserID, j.Kind, j.Priority, "queued", j.Positive, j.Negative, j.Combo,
		j.Width, j.Height, j.Seed, j.WorkflowID, now, now)
	return err
}

// Job 查询任务。
func (s *Store) Job(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT job_id, user_id, kind, priority, status, COALESCE(error,''),
positive, negative, COALESCE(combo,''), width, height, seed, workflow_id, COALESCE(image_path,'')
FROM jobs WHERE job_id=?`, id)
	var j Job
	if err := row.Scan(&j.JobID, &j.UserID, &j.Kind, &j.Priority, &j.Status, &j.Error,
		&j.Positive, &j.Negative, &j.Combo, &j.Width, &j.Height, &j.Seed, &j.WorkflowID, &j.ImagePath); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

// NextQueuedJob 取下一个可执行任务: 优先 normal; idleOK 时才取 idle。
func (s *Store) NextQueuedJob(idleOK bool) (*Job, error) {
	row := s.db.QueryRow(`SELECT job_id, user_id, kind, priority, status, positive, negative, COALESCE(combo,''), width, height, seed, workflow_id
FROM jobs WHERE status='queued' AND priority='normal' ORDER BY created_at LIMIT 1`)
	j, err := scanQueued(row)
	if err == nil && j != nil {
		return j, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	if !idleOK {
		return nil, nil
	}
	row = s.db.QueryRow(`SELECT job_id, user_id, kind, priority, status, positive, negative, COALESCE(combo,''), width, height, seed, workflow_id
FROM jobs WHERE status='queued' AND priority='idle' ORDER BY created_at LIMIT 1`)
	j, err = scanQueued(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func scanQueued(row *sql.Row) (*Job, error) {
	var j Job
	if err := row.Scan(&j.JobID, &j.UserID, &j.Kind, &j.Priority, &j.Status,
		&j.Positive, &j.Negative, &j.Combo, &j.Width, &j.Height, &j.Seed, &j.WorkflowID); err != nil {
		return nil, err
	}
	return &j, nil
}

// SetJobStatus 更新任务状态。
func (s *Store) SetJobStatus(id, status, errMsg, imagePath string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?, error=?, image_path=?, updated_at=? WHERE job_id=?`,
		status, errMsg, imagePath, time.Now().Format(timeFmt), id)
	return err
}

// PurgeJobs 清理 N 天前的终态任务。
func (s *Store) PurgeJobs(before time.Time) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE status IN ('done','failed') AND updated_at < ?`,
		before.Format(timeFmt))
	return err
}

// ---------- pregen ----------

// Pregen 预生成状态。
type Pregen struct {
	UserID         string
	ProfileVersion int
	JobID          string
	Ready          bool
	Positive       string
	Negative       string
	Combo          string
	Seed           int64
	WorkflowID     string
	Width          int
	Height         int
	ImagePath      string
}

// UpsertPregen 覆盖式写入预生成请求(旧成品作废)。
func (s *Store) UpsertPregen(p Pregen) error {
	_, err := s.db.Exec(`INSERT INTO pregen(user_id, profile_version, job_id, ready, positive, negative, combo, seed, workflow_id, width, height, image_path, updated_at)
VALUES(?,?,?,0,?,?,?,?,?,?,?, '', ?)
ON CONFLICT(user_id) DO UPDATE SET profile_version=excluded.profile_version, job_id=excluded.job_id,
ready=0, positive=excluded.positive, negative=excluded.negative, combo=excluded.combo, seed=excluded.seed,
workflow_id=excluded.workflow_id, width=excluded.width, height=excluded.height, image_path='', updated_at=excluded.updated_at`,
		p.UserID, p.ProfileVersion, p.JobID, p.Positive, p.Negative, p.Combo, p.Seed, p.WorkflowID,
		p.Width, p.Height, time.Now().Format(timeFmt))
	return err
}

// GetPregen 读取预生成状态。
func (s *Store) GetPregen(userID string) (*Pregen, error) {
	row := s.db.QueryRow(`SELECT user_id, profile_version, COALESCE(job_id,''), ready, positive, negative, COALESCE(combo,''), seed, workflow_id, width, height, COALESCE(image_path,'')
FROM pregen WHERE user_id=?`, userID)
	var p Pregen
	var ready int
	if err := row.Scan(&p.UserID, &p.ProfileVersion, &p.JobID, &ready, &p.Positive, &p.Negative,
		&p.Combo, &p.Seed, &p.WorkflowID, &p.Width, &p.Height, &p.ImagePath); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.Ready = ready != 0
	return &p, nil
}

// SetPregenReady 预生成完成。
func (s *Store) SetPregenReady(userID, imagePath string) error {
	_, err := s.db.Exec(`UPDATE pregen SET ready=1, image_path=?, updated_at=? WHERE user_id=?`,
		imagePath, time.Now().Format(timeFmt), userID)
	return err
}

// ---------- link tickets ----------

// CreateTicket 生成一次性配对票据。
func (s *Store) CreateTicket(code, userID string, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO link_tickets(code, user_id, created_at, expires_at, used) VALUES(?,?,?,?,0)`,
		code, userID, time.Now().Format(timeFmt), expires.Format(timeFmt))
	return err
}

// RedeemTicket 兑换配对码, 返回目标 user_id; ok=false 表示无效/过期/已用。
func (s *Store) RedeemTicket(code string, now time.Time) (string, bool, error) {
	row := s.db.QueryRow(`SELECT user_id, expires_at, used FROM link_tickets WHERE code=?`, code)
	var userID, exp string
	var used int
	if err := row.Scan(&userID, &exp, &used); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	expT, err := time.Parse(timeFmt, exp)
	if err != nil || used != 0 || now.After(expT) {
		return "", false, nil
	}
	if _, err := s.db.Exec(`UPDATE link_tickets SET used=1 WHERE code=?`, code); err != nil {
		return "", false, err
	}
	return userID, true, nil
}

// ---------- drift ----------

// UpsertDrift 保存漂移摘要。
func (s *Store) UpsertDrift(userID, summary string) error {
	_, err := s.db.Exec(`INSERT INTO drift_reports(user_id, summary, updated_at) VALUES(?,?,?)
ON CONFLICT(user_id) DO UPDATE SET summary=excluded.summary, updated_at=excluded.updated_at`,
		userID, summary, time.Now().Format(timeFmt))
	return err
}

// GetDrift 读取漂移摘要。
func (s *Store) GetDrift(userID string) (string, error) {
	var summary string
	err := s.db.QueryRow(`SELECT summary FROM drift_reports WHERE user_id=?`, userID).Scan(&summary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return summary, err
}

// AllUsers 管理看板用。
func (s *Store) AllUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT user_id, token, created_at, disabled FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled int
		if err := rows.Scan(&u.UserID, &u.Token, &u.CreatedAt, &disabled); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
