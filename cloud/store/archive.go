package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ArchiveRow 归档记录(用户图片历史): jobs 7 天即 Purge, 归档独立存活直到用户删除。
type ArchiveRow struct {
	JobID      string
	UserID     string
	Kind       string // gen | pregen
	CreatedAt  string
	Width      int
	Height     int
	Seed       int64
	Positive   string
	WorkflowID string
	OssKey     string
	ImagePath  string
	Fav        int
}

const archiveCols = `job_id, user_id, kind, created_at, width, height, seed,
positive, workflow_id, oss_key, image_path, fav`

func scanArchive(row *sql.Row) (*ArchiveRow, error) {
	var a ArchiveRow
	if err := row.Scan(&a.JobID, &a.UserID, &a.Kind, &a.CreatedAt, &a.Width, &a.Height,
		&a.Seed, &a.Positive, &a.WorkflowID, &a.OssKey, &a.ImagePath, &a.Fav); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// ArchiveJob gen 完成落归档(幂等; pregen 不在此落——提货时才归档, 未提货的不算用户历史)。
func (s *Store) ArchiveJob(jobID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO archive
(job_id, user_id, kind, created_at, width, height, seed, positive, workflow_id, oss_key, image_path)
SELECT job_id, user_id, kind, created_at, width, height, seed,
       COALESCE(positive,''), COALESCE(workflow_id,''), COALESCE(oss_key,''), COALESCE(image_path,'')
FROM jobs WHERE job_id=? AND status='done' AND kind='gen'
  AND (COALESCE(image_path,'')<>'' OR COALESCE(oss_key,'')<>'')`, jobID)
	return err
}

// ArchivePregen pregen 提货落归档(行已弹出, 用弹出值; created_at=交付时刻)。
func (s *Store) ArchivePregen(p Pregen) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO archive
(job_id, user_id, kind, created_at, width, height, seed, positive, workflow_id, oss_key, image_path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		p.JobID, p.UserID, "pregen", time.Now().Format(timeFmt),
		p.Width, p.Height, p.Seed, p.Positive, p.WorkflowID, p.OssKey, p.ImagePath)
	return err
}

// Archive 查单条归档(删除联动/历史取图), 不存在返回 (nil, nil)。
func (s *Store) Archive(jobID string) (*ArchiveRow, error) {
	return scanArchive(s.db.QueryRow(`SELECT `+archiveCols+` FROM archive WHERE job_id=?`, jobID))
}

// ListArchive 用户历史(新→旧)。
func (s *Store) ListArchive(userID string, limit int) ([]ArchiveRow, error) {
	rows, err := s.db.Query(`SELECT `+archiveCols+` FROM archive WHERE user_id=?
ORDER BY created_at DESC, rowid DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveRow{}
	for rows.Next() {
		var a ArchiveRow
		if err := rows.Scan(&a.JobID, &a.UserID, &a.Kind, &a.CreatedAt, &a.Width, &a.Height,
			&a.Seed, &a.Positive, &a.WorkflowID, &a.OssKey, &a.ImagePath, &a.Fav); err != nil {
			return nil, fmt.Errorf("scan archive: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteArchive 删除归档行(用户删除图片时联动)。
func (s *Store) DeleteArchive(jobID string) error {
	_, err := s.db.Exec(`DELETE FROM archive WHERE job_id=?`, jobID)
	return err
}

// ClearPregenImage 清预生成成品引用(删除联动: 该单不再可提货)。
func (s *Store) ClearPregenImage(jobID string) error {
	_, err := s.db.Exec(`UPDATE pregen SET ready=0, image_path='', oss_key='', updated_at=? WHERE job_id=?`,
		time.Now().Format(timeFmt), jobID)
	return err
}
