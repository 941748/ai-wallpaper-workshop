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
	// 版本表: 记录已应用的迁移版本(既有生产库无此表 → 视为版本 0, 顺序补齐;
	// 所有迁移均写成幂等(IF NOT EXISTS / 守卫式 ALTER / 主键探测重建), 对旧库重放安全)。
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL)`); err != nil {
		return err
	}
	cur, err := s.schemaVersion()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= cur {
			continue
		}
		if err := m.apply(s.db); err != nil {
			return fmt.Errorf("store 迁移 v%d 失败: %w", m.version, err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, m.version); err != nil {
			return fmt.Errorf("store 迁移 v%d 版本落库失败: %w", m.version, err)
		}
	}
	return nil
}

// schemaVersion 返回已应用的最高迁移版本(空表视为 0)。
func (s *Store) schemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

// migration 一次顺序迁移(版本号严格递增)。
type migration struct {
	version int
	apply   func(*sql.DB) error
}

// migrations 顺序迁移表: 新增迁移时追加更高版本, 勿改动既有项(保证旧库重放一致)。
var migrations = []migration{
	{1, m001BaselineSchema},
	{2, m002UserLastActive},
	{3, m003UserProfiles},
	{4, m004Pregen},
	{5, m005SiteEventIP},
	{6, m006ProfilePacks},
	{7, m007UserClientVer},
	{8, m008DeviceAttrs},
	{9, m009OssKey},
	{10, m0010JobRender},
	{11, m011Archive},
}

// m001BaselineSchema 基线表结构(全部 IF NOT EXISTS, 对新库建表/对旧库无操作)。
func m001BaselineSchema(db *sql.DB) error {
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
	_, err := db.Exec(schema)
	return err
}

// m002UserLastActive 为旧库补 users.last_active 列(活跃窗口判定用); 已含则跳过。
func m002UserLastActive(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='last_active'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE users ADD COLUMN last_active TEXT NOT NULL DEFAULT ''`)
	return err
}

// m003UserProfiles 画像快照表: 服务端空闲主动预生成所需的最近画像。
func m003UserProfiles(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_profiles(
  user_id         TEXT PRIMARY KEY,
  profile_version INTEGER NOT NULL,
  profile         TEXT,
  disliked        TEXT,
  custom          TEXT,
  screen_w        INTEGER NOT NULL DEFAULT 0,
  screen_h        INTEGER NOT NULL DEFAULT 0,
  updated_at      TEXT NOT NULL
)`)
	return err
}

// m004Pregen 建(或确保)新版 pregen 表: 以 job_id 为主键, 支持每用户多单并存
// (补池"一次出多张"); 检测到旧版主键(user_id, 每用户单行)时重建。表内容仅为待提货
// 短周期缓冲, 重建后客户端下轮自动重新补池, 无实质损失。
// 注意: 索引必须在表结构确认为新版后创建(旧表无 created_at 列)。
func m004Pregen(db *sql.DB) error {
	const ddl = `CREATE TABLE IF NOT EXISTS pregen (
  job_id          TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL,
  profile_version INTEGER NOT NULL,
  ready           INTEGER NOT NULL DEFAULT 0,
  positive        TEXT,
  negative        TEXT,
  combo           TEXT,
  seed            INTEGER,
  workflow_id     TEXT,
  width           INTEGER,
  height          INTEGER,
  image_path      TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
)`
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	var pkCol string
	if err := db.QueryRow(`SELECT name FROM pragma_table_info('pregen') WHERE pk>0 LIMIT 1`).Scan(&pkCol); err == nil && pkCol != "" && pkCol != "job_id" {
		if _, err := db.Exec(`DROP TABLE pregen`); err != nil {
			return err
		}
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_pregen_user ON pregen(user_id, ready, created_at)`)
	return err
}

// m005SiteEventIP 为 site_events 补 ip 列(访客来源 IP, 供后续分析客户区域); 已含则跳过。
func m005SiteEventIP(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_events') WHERE name='ip'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE site_events ADD COLUMN ip TEXT NOT NULL DEFAULT ''`)
	return err
}

// m006ProfilePacks 画像快照表补 packs 列: 风格包(一图一包)必须随快照同步,
// 否则服务端主动预生成在包党画像(权重全 0.3、推不出包)下逐维采样, 满词典乱出
// (枪械/飞机/漫画等非偏好题材)。守卫式 ALTER(与 m005 同式), 重放安全。
func m006ProfilePacks(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('user_profiles') WHERE name='packs'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE user_profiles ADD COLUMN packs TEXT NOT NULL DEFAULT ''`)
	return err
}

// m007UserClientVer users 加客户端版本两列(更新检查时上报)。逐列守卫: 任一列缺失即补, 避免半途失败后早退。
func m007UserClientVer(db *sql.DB) error {
	for _, col := range []struct{ name, ddl string }{
		{"client_ver", `ALTER TABLE users ADD COLUMN client_ver TEXT NOT NULL DEFAULT ''`},
		{"client_ver_at", `ALTER TABLE users ADD COLUMN client_ver_at TEXT NOT NULL DEFAULT ''`},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name=?`, col.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(col.ddl); err != nil {
			return err
		}
	}
	return nil
}

// SetClientVersion 记录用户最近一次上报的客户端版本(更新检查 /client/latest 时调用)。
func (s *Store) SetClientVersion(userID, ver string) error {
	_, err := s.db.Exec(`UPDATE users SET client_ver=?, client_ver_at=? WHERE user_id=?`,
		ver, time.Now().Format(timeFmt), userID)
	return err
}

// m008DeviceAttrs devices 加设备属性/屏幕列(安卓配套, 方案A: screen 按设备存)。
// 顺序: 逐列加 → 旧数据回填 type=pc(现存设备全为 Windows 客户端) → 去重 → 建 (device,user_id) 唯一索引
// (索引必须在去重之后, 否则存量重复行会令 CREATE UNIQUE INDEX 失败)。
func m008DeviceAttrs(db *sql.DB) error {
	for _, col := range []struct{ name, ddl string }{
		{"device_type", `ALTER TABLE devices ADD COLUMN device_type TEXT NOT NULL DEFAULT ''`},
		{"model", `ALTER TABLE devices ADD COLUMN model TEXT NOT NULL DEFAULT ''`},
		{"os", `ALTER TABLE devices ADD COLUMN os TEXT NOT NULL DEFAULT ''`},
		{"os_version", `ALTER TABLE devices ADD COLUMN os_version TEXT NOT NULL DEFAULT ''`},
		{"app_version", `ALTER TABLE devices ADD COLUMN app_version TEXT NOT NULL DEFAULT ''`},
		{"screen_w", `ALTER TABLE devices ADD COLUMN screen_w INTEGER NOT NULL DEFAULT 0`},
		{"screen_h", `ALTER TABLE devices ADD COLUMN screen_h INTEGER NOT NULL DEFAULT 0`},
		{"last_seen", `ALTER TABLE devices ADD COLUMN last_seen TEXT NOT NULL DEFAULT ''`},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name=?`, col.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(col.ddl); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE devices SET device_type='pc' WHERE device_type=''`); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM devices WHERE rowid NOT IN
(SELECT MIN(rowid) FROM devices GROUP BY device, user_id)`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_du ON devices(device, user_id)`)
	return err
}

// m009OssKey jobs/pregen 加 oss_key(OSS 对象键; 非空=图片在 OSS, 端点302签名直下, 空=本地文件回退)。
func m009OssKey(db *sql.DB) error {
	for _, c := range []struct{ table, name string }{
		{"jobs", "oss_key"},
		{"pregen", "oss_key"},
	} {
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name=?`, c.table)
		if err := db.QueryRow(q, c.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		ddl := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN oss_key TEXT NOT NULL DEFAULT ''`, c.table)
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// SetJobOssKey 记录任务成品的 OSS 对象键(出图机代理上传完成后回报)。
func (s *Store) SetJobOssKey(id, ossKey string) error {
	_, err := s.db.Exec(`UPDATE jobs SET oss_key=?, updated_at=? WHERE job_id=?`,
		ossKey, time.Now().Format(timeFmt), id)
	return err
}

// SetPregenOssKey 记录预生成成品的 OSS 对象键。
func (s *Store) SetPregenOssKey(jobID, ossKey string) error {
	_, err := s.db.Exec(`UPDATE pregen SET oss_key=?, updated_at=? WHERE job_id=?`,
		ossKey, time.Now().Format(timeFmt), jobID)
	return err
}

// ClearJobImage 清除任务成品引用(用户删除图片: OSS 对象与本地文件已删, 行保留供统计)。
func (s *Store) ClearJobImage(id string) error {
	_, err := s.db.Exec(`UPDATE jobs SET image_path='', oss_key='', updated_at=? WHERE job_id=?`,
		time.Now().Format(timeFmt), id)
	return err
}

// DeviceAttr 设备属性(register/link 携带的 device_info)。
type DeviceAttr struct {
	Device, UserID                     string
	Type, Model, OS, OSVersion, AppVer string
	ScreenW, ScreenH                   int
}

// AddDevice 登记设备(兼容旧调用): 按 (device,user_id) 幂等插入, 仅刷新活跃时间; 存量流量全为 PC。
func (s *Store) AddDevice(device, userID string) error {
	now := time.Now().Format(timeFmt)
	_, err := s.db.Exec(`INSERT INTO devices(device, user_id, created_at, last_seen, device_type)
VALUES(?,?,?,?, 'pc')
ON CONFLICT(device, user_id) DO UPDATE SET last_seen=excluded.last_seen`,
		device, userID, now, now)
	return err
}

// UpsertDeviceAttrs 登记/更新设备属性与屏幕(device_info 上报; 空值字段不覆盖已有值)。
func (s *Store) UpsertDeviceAttrs(d DeviceAttr) error {
	now := time.Now().Format(timeFmt)
	_, err := s.db.Exec(`INSERT INTO devices
(device, user_id, created_at, last_seen, device_type, model, os, os_version, app_version, screen_w, screen_h)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(device, user_id) DO UPDATE SET
  device_type=CASE WHEN excluded.device_type<>'' THEN excluded.device_type ELSE devices.device_type END,
  model=CASE WHEN excluded.model<>'' THEN excluded.model ELSE devices.model END,
  os=CASE WHEN excluded.os<>'' THEN excluded.os ELSE devices.os END,
  os_version=CASE WHEN excluded.os_version<>'' THEN excluded.os_version ELSE devices.os_version END,
  app_version=CASE WHEN excluded.app_version<>'' THEN excluded.app_version ELSE devices.app_version END,
  screen_w=CASE WHEN excluded.screen_w>0 THEN excluded.screen_w ELSE devices.screen_w END,
  screen_h=CASE WHEN excluded.screen_h>0 THEN excluded.screen_h ELSE devices.screen_h END,
  last_seen=excluded.last_seen`,
		d.Device, d.UserID, now, now, d.Type, d.Model, d.OS, d.OSVersion, d.AppVer, d.ScreenW, d.ScreenH)
	return err
}

// TouchDeviceScreen 出图请求随行 device_id: 刷新屏幕(仅正值)与活跃时间; 只更新已登记行。
func (s *Store) TouchDeviceScreen(device, userID string, w, h int) error {
	now := time.Now().Format(timeFmt)
	if w > 0 && h > 0 {
		_, err := s.db.Exec(`UPDATE devices SET screen_w=?, screen_h=?, last_seen=? WHERE device=? AND user_id=?`,
			w, h, now, device, userID)
		return err
	}
	_, err := s.db.Exec(`UPDATE devices SET last_seen=? WHERE device=? AND user_id=?`, now, device, userID)
	return err
}

// DeviceIsAndroid 设备是否安卓端(device_info 上报过 type=android); 未登记返回 false。
func (s *Store) DeviceIsAndroid(device, userID string) (bool, error) {
	var t string
	err := s.db.QueryRow(`SELECT device_type FROM devices WHERE device=? AND user_id=?`, device, userID).Scan(&t)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t == "android", nil
}

// RecentPCScreen 最近活跃 PC 设备的屏幕尺寸(无则 0,0)。预生成池只服务 PC, 以设备维度为准。
func (s *Store) RecentPCScreen(userID string) (w, h int, err error) {
	err = s.db.QueryRow(`SELECT screen_w, screen_h FROM devices
WHERE user_id=? AND device_type='pc' AND screen_w>0 AND screen_h>0
ORDER BY COALESCE(NULLIF(last_seen,''), created_at) DESC LIMIT 1`, userID).Scan(&w, &h)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return
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

// TouchUser 刷新用户活跃时间(每次认证请求调用)。
func (s *Store) TouchUser(userID string) error {
	_, err := s.db.Exec(`UPDATE users SET last_active=? WHERE user_id=?`,
		time.Now().Format(timeFmt), userID)
	return err
}

// ListActiveUsers 返回 last_active >= since 且未禁用的用户 ID(空闲主动预生成用)。
func (s *Store) ListActiveUsers(since time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT user_id FROM users WHERE disabled=0 AND last_active >= ?
ORDER BY last_active DESC`, since.Format(timeFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ProfileSnapshot 用户最近一次随行画像快照(服务端主动预生成用)。
type ProfileSnapshot struct {
	UserID         string
	ProfileVersion int
	Profile        string // JSON map[string]float64
	Disliked       string // JSON []string
	Custom         string // JSON []string
	Packs          string // JSON []string 风格包名(""=旧快照无此字段, nil→权重反推)
	ScreenW        int
	ScreenH        int
}

// SaveProfileSnapshot upsert 画像快照。
func (s *Store) SaveProfileSnapshot(p ProfileSnapshot) error {
	_, err := s.db.Exec(`INSERT INTO user_profiles
(user_id, profile_version, profile, disliked, custom, packs, screen_w, screen_h, updated_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET profile_version=excluded.profile_version, profile=excluded.profile,
 disliked=excluded.disliked, custom=excluded.custom, packs=excluded.packs,
 screen_w=excluded.screen_w, screen_h=excluded.screen_h, updated_at=excluded.updated_at`,
		p.UserID, p.ProfileVersion, p.Profile, p.Disliked, p.Custom, p.Packs,
		p.ScreenW, p.ScreenH, time.Now().Format(timeFmt))
	return err
}

// LoadProfileSnapshot 读取画像快照(无则返回 nil)。
func (s *Store) LoadProfileSnapshot(userID string) (*ProfileSnapshot, error) {
	row := s.db.QueryRow(`SELECT user_id, profile_version, COALESCE(profile,''), COALESCE(disliked,''),
COALESCE(custom,''), COALESCE(packs,''), screen_w, screen_h FROM user_profiles WHERE user_id=?`, userID)
	var p ProfileSnapshot
	if err := row.Scan(&p.UserID, &p.ProfileVersion, &p.Profile, &p.Disliked, &p.Custom, &p.Packs, &p.ScreenW, &p.ScreenH); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// AddDevice 记录设备。
// (旧 AddDevice 已由下方带 upsert 的实现替代, 迁移 v8 建 (device,user_id) 唯一索引后幂等)

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
	Kind       string // gen | pregen
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
	OssKey     string // OSS 对象键(非空=图片在 OSS, 下发302签名URL)
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
positive, negative, COALESCE(combo,''), width, height, seed, workflow_id, COALESCE(image_path,''), COALESCE(oss_key,'')
FROM jobs WHERE job_id=?`, id)
	var j Job
	if err := row.Scan(&j.JobID, &j.UserID, &j.Kind, &j.Priority, &j.Status, &j.Error,
		&j.Positive, &j.Negative, &j.Combo, &j.Width, &j.Height, &j.Seed, &j.WorkflowID, &j.ImagePath, &j.OssKey); err != nil {
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

// PurgeJobsByKind 删除指定类型的全部任务(含 queued/running)。
// 用于机制退役后的存量清理(如探针池遗留的 probe 任务, 避免残留任务被 worker 消费)。
func (s *Store) PurgeJobsByKind(kind string) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE kind = ?`, kind)
	return err
}

// ---------- pregen ----------

// maxPregenPerUser 每用户保留的预生成条数上限(防御异常积累; 正常节奏由客户端补池与提货删除控制)。
const maxPregenPerUser = 6

// Pregen 预生成状态(一次预生成=一行, 以 job_id 唯一)。
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
	OssKey         string // OSS 对象键(非空=图片在 OSS)
}

const pregenCols = `job_id, user_id, profile_version, ready, positive, negative, COALESCE(combo,''), seed, workflow_id, width, height, COALESCE(image_path,''), COALESCE(oss_key,'')`

func scanPregen(row *sql.Row) (*Pregen, error) {
	var p Pregen
	var ready int
	if err := row.Scan(&p.JobID, &p.UserID, &p.ProfileVersion, &ready, &p.Positive, &p.Negative,
		&p.Combo, &p.Seed, &p.WorkflowID, &p.Width, &p.Height, &p.ImagePath, &p.OssKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.Ready = ready != 0
	return &p, nil
}

// InsertPregen 写入一条预生成请求(每单一行, 不覆盖已有单)。
func (s *Store) InsertPregen(p Pregen) error {
	now := time.Now().Format(timeFmt)
	_, err := s.db.Exec(`INSERT INTO pregen(job_id, user_id, profile_version, ready, positive, negative, combo, seed, workflow_id, width, height, image_path, created_at, updated_at)
VALUES(?,?,?,0,?,?,?,?,?,?,?, '', ?, ?)`,
		p.JobID, p.UserID, p.ProfileVersion, p.Positive, p.Negative, p.Combo, p.Seed, p.WorkflowID,
		p.Width, p.Height, now, now)
	if err != nil {
		return err
	}
	// 每用户上限: 超出时清理最旧(异常积累不影响正常滚动)
	_, _ = s.db.Exec(`DELETE FROM pregen WHERE user_id=? AND job_id NOT IN (
		SELECT job_id FROM pregen WHERE user_id=? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		p.UserID, p.UserID, maxPregenPerUser)
	return nil
}

// PeekPregenReady 取该用户最早就绪的一条(profileVersion>0 时按画像版本过滤), 不删除。
func (s *Store) PeekPregenReady(userID string, profileVersion int) (*Pregen, error) {
	q := `SELECT ` + pregenCols + ` FROM pregen WHERE user_id=? AND ready=1`
	args := []any{userID}
	if profileVersion > 0 {
		q += ` AND profile_version=?`
		args = append(args, profileVersion)
	}
	q += ` ORDER BY created_at ASC, rowid ASC LIMIT 1`
	return scanPregen(s.db.QueryRow(q, args...))
}

// PeekPregenByJob 查指定单状态(校验归属, 不删除)。
func (s *Store) PeekPregenByJob(userID, jobID string) (*Pregen, error) {
	return scanPregen(s.db.QueryRow(`SELECT `+pregenCols+` FROM pregen WHERE user_id=? AND job_id=?`, userID, jobID))
}

// PopPregenReady 原子取走该用户"当前画像版本(ready 行中最大版本)最早就绪"的一条, 老客户端无 job_id 路径。
// 老客户端提货不带版本参数, 取最大版本可避免弹到改偏好前残留的旧版本成品。
func (s *Store) PopPregenReady(userID string) (*Pregen, error) {
	return s.popPregen(userID, "")
}

// PopPregenByJob 原子取走指定单(校验归属), 新版客户端按 job_id 提货。
func (s *Store) PopPregenByJob(userID, jobID string) (*Pregen, error) {
	return s.popPregen(userID, jobID)
}

func (s *Store) popPregen(userID, jobID string) (*Pregen, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := `SELECT ` + pregenCols + ` FROM pregen WHERE user_id=? AND ready=1`
	args := []any{userID}
	if jobID != "" {
		q += ` AND job_id=?`
		args = append(args, jobID)
	} else {
		q += ` AND profile_version=(SELECT COALESCE(MAX(profile_version),0) FROM pregen p2 WHERE p2.user_id=? AND p2.ready=1)`
		args = append(args, userID)
	}
	q += ` ORDER BY created_at ASC, rowid ASC LIMIT 1`
	p, err := scanPregen(tx.QueryRow(q, args...))
	if err != nil || p == nil {
		return p, err
	}
	if _, err := tx.Exec(`DELETE FROM pregen WHERE job_id=?`, p.JobID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

// SetPregenReady 预生成完成(按 job_id 定位)。
func (s *Store) SetPregenReady(jobID, imagePath string) error {
	_, err := s.db.Exec(`UPDATE pregen SET ready=1, image_path=?, updated_at=? WHERE job_id=?`,
		imagePath, time.Now().Format(timeFmt), jobID)
	return err
}

// CountPregens 该用户当前预生成条数(测试/诊断)。
func (s *Store) CountPregens(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pregen WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

// CountPregenReady 该用户指定画像版本已就绪待提的预生成数。
func (s *Store) CountPregenReady(userID string, profileVersion int) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pregen WHERE user_id=? AND ready=1 AND profile_version=?`,
		userID, profileVersion).Scan(&n)
	return n, err
}

// CountPregenPending 该用户在途(未就绪)预生成单数。
func (s *Store) CountPregenPending(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pregen WHERE user_id=? AND ready=0`, userID).Scan(&n)
	return n, err
}

// CountReadyPregens 该用户已就绪的预生成条数(测试/诊断)。
func (s *Store) CountReadyPregens(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pregen WHERE user_id=? AND ready=1`, userID).Scan(&n)
	return n, err
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

// ---------- P1 OSS 直传(出图机代理) ----------

// m0010JobRender jobs 加渲染引用(prompt_id+Comfy 文件名), 供代理取图与超时兜底。
func m0010JobRender(db *sql.DB) error {
	for _, c := range []struct{ name, ddl string }{
		{"prompt_id", `ALTER TABLE jobs ADD COLUMN prompt_id TEXT NOT NULL DEFAULT ''`},
		{"image_name", `ALTER TABLE jobs ADD COLUMN image_name TEXT NOT NULL DEFAULT ''`},
		{"image_sub", `ALTER TABLE jobs ADD COLUMN image_sub TEXT NOT NULL DEFAULT ''`},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name=?`, c.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// SetJobRender 渲染完成待上传: 记录 prompt/文件引用并置 oss_pending(updated_at 作为代理时效起点)。
func (s *Store) SetJobRender(jobID, promptID, imageName, imageSub string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status='oss_pending', error='', prompt_id=?, image_name=?, image_sub=?, updated_at=?
WHERE job_id=?`, promptID, imageName, imageSub, time.Now().Format(timeFmt), jobID)
	return err
}

// m011Archive 归档表(用户图片历史): jobs 7 天即 Purge, 历史须独立存活;
// 回填存量 done 任务(pregen 仅回填已提货的=行已从 pregen 弹出), fav 列为 S2④ 收藏预留。
func m011Archive(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS archive (
  job_id     TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  kind       TEXT NOT NULL DEFAULT 'gen',
  created_at TEXT NOT NULL,
  width      INTEGER NOT NULL DEFAULT 0,
  height     INTEGER NOT NULL DEFAULT 0,
  seed       INTEGER NOT NULL DEFAULT 0,
  positive   TEXT NOT NULL DEFAULT '',
  workflow_id TEXT NOT NULL DEFAULT '',
  oss_key    TEXT NOT NULL DEFAULT '',
  image_path TEXT NOT NULL DEFAULT '',
  fav        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_archive_user ON archive(user_id, created_at);
INSERT OR IGNORE INTO archive
(job_id, user_id, kind, created_at, width, height, seed, positive, workflow_id, oss_key, image_path)
SELECT job_id, user_id, kind, created_at, width, height, seed,
       COALESCE(positive,''), COALESCE(workflow_id,''), COALESCE(oss_key,''), COALESCE(image_path,'')
FROM jobs
WHERE status='done'
  AND (COALESCE(image_path,'')<>'' OR COALESCE(oss_key,'')<>'')
  AND (kind<>'pregen' OR NOT EXISTS (SELECT 1 FROM pregen p WHERE p.job_id=jobs.job_id))`)
	return err
}

// OssTask 代理待传任务。
type OssTask struct {
	JobID     string
	UserID    string
	Kind      string
	OssKey    string // 服务端指定的对象键(代理按此上传)
	ImageName string
	ImageSub  string
	PromptID  string
}

// OssKeyFor 按用户生成对象键(S2 起统一为 u/{user_id}/{jobID}.jpg: 便于按用户
// 列举/删除/归档保留; kind 不再体现在键上, 类型信息在库里)。
func OssKeyFor(userID, jobID string) string {
	return "u/" + userID + "/" + jobID + ".jpg"
}

// ListOssPending 代理拉取的待传清单(oss_pending 全部)。
func (s *Store) ListOssPending() ([]OssTask, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, kind, prompt_id, image_name, COALESCE(image_sub,'')
FROM jobs WHERE status='oss_pending' ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OssTask
	for rows.Next() {
		var t OssTask
		if err := rows.Scan(&t.JobID, &t.UserID, &t.Kind, &t.PromptID, &t.ImageName, &t.ImageSub); err != nil {
			return nil, err
		}
		t.OssKey = OssKeyFor(t.UserID, t.JobID)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListStaleOssPending 超时未获代理确认的任务(兜底拉图用)。
func (s *Store) ListStaleOssPending(cutoff time.Time) ([]OssTask, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, kind, prompt_id, image_name, COALESCE(image_sub,'')
FROM jobs WHERE status='oss_pending' AND updated_at < ? ORDER BY updated_at ASC`, cutoff.Format(timeFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OssTask
	for rows.Next() {
		var t OssTask
		if err := rows.Scan(&t.JobID, &t.UserID, &t.Kind, &t.PromptID, &t.ImageName, &t.ImageSub); err != nil {
			return nil, err
		}
		t.OssKey = OssKeyFor(t.UserID, t.JobID)
		out = append(out, t)
	}
	return out, rows.Err()
}
