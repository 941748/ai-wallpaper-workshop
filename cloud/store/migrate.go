package store

// S2 存量迁移查询(只读, 供云服务后台迁移任务使用)。

// MigJob 迁移工作项(本地待传 OSS 或待重键)。
type MigJob struct {
	JobID     string
	UserID    string
	Kind      string
	ImagePath string
	OssKey    string
	Width     int
	Height    int
}

// LocalUnmigratedJobs 有本地文件但还没 oss_key 的任务(S2a Phase1: 本地文件上传)。
func (s *Store) LocalUnmigratedJobs() ([]MigJob, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, kind, COALESCE(image_path,''), COALESCE(oss_key,''),
COALESCE(width,0), COALESCE(height,0)
FROM jobs WHERE image_path<>'' AND oss_key='' ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMig(rows)
}

// LocalUnmigratedPregens pregen 行有本地文件但没 oss_key(job 行可能已迁, 补迁行)。
func (s *Store) LocalUnmigratedPregens() ([]MigJob, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, 'pregen', COALESCE(image_path,''), COALESCE(oss_key,''),
COALESCE(width,0), COALESCE(height,0)
FROM pregen WHERE image_path<>'' AND oss_key='' ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMig(rows)
}

// OssRekeyCandidates 已在 OSS 但仍是旧键(不带 u/ 前缀)的任务。
func (s *Store) OssRekeyCandidates() ([]MigJob, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, kind, '', COALESCE(oss_key,''),
COALESCE(width,0), COALESCE(height,0)
FROM jobs WHERE oss_key<>'' AND oss_key NOT LIKE 'u/%' ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMig(rows)
}

// OssRekeyPregens pregen 行旧键(job 行可能已被清理)。
func (s *Store) OssRekeyPregens() ([]MigJob, error) {
	rows, err := s.db.Query(`SELECT job_id, user_id, 'pregen', '', COALESCE(oss_key,''),
COALESCE(width,0), COALESCE(height,0)
FROM pregen WHERE oss_key<>'' AND oss_key NOT LIKE 'u/%' ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMig(rows)
}

// ReferencedImagePaths 库里仍引用的本地文件全集(孤儿判定基准; 归档长期存活, 其本地兜底文件不可清)。
func (s *Store) ReferencedImagePaths() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT image_path FROM jobs WHERE image_path<>''
UNION SELECT image_path FROM pregen WHERE image_path<>''
UNION SELECT image_path FROM archive WHERE image_path<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

func scanMig(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]MigJob, error) {
	var out []MigJob
	for rows.Next() {
		var m MigJob
		if err := rows.Scan(&m.JobID, &m.UserID, &m.Kind, &m.ImagePath, &m.OssKey, &m.Width, &m.Height); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
