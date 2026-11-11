package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ArchiveRow 归档记录(用户图片历史): jobs 7 天即 Purge, 归档独立存活直到用户删除。
// 配方字段(positive/negative/combo/seed/workflow)随落档保存, 支撑展示与"同款复现"。
type ArchiveRow struct {
	JobID      string
	UserID     string
	Kind       string // gen | pregen
	CreatedAt  string
	Width      int
	Height     int
	Seed       int64
	Positive   string
	Negative   string
	Combo      string
	WorkflowID string
	OssKey     string
	ImagePath  string
	Fav        int
}

const archiveCols = `job_id, user_id, kind, created_at, width, height, seed,
positive, negative, combo, workflow_id, oss_key, image_path, fav`

func scanArchive(row *sql.Row) (*ArchiveRow, error) {
	var a ArchiveRow
	if err := row.Scan(&a.JobID, &a.UserID, &a.Kind, &a.CreatedAt, &a.Width, &a.Height,
		&a.Seed, &a.Positive, &a.Negative, &a.Combo, &a.WorkflowID, &a.OssKey, &a.ImagePath, &a.Fav); err != nil {
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
(job_id, user_id, kind, created_at, width, height, seed, positive, negative, combo, workflow_id, oss_key, image_path)
SELECT job_id, user_id, kind, created_at, width, height, seed,
       COALESCE(positive,''), COALESCE(negative,''), COALESCE(combo,''),
       COALESCE(workflow_id,''), COALESCE(oss_key,''), COALESCE(image_path,'')
FROM jobs WHERE job_id=? AND status='done' AND kind='gen'
  AND (COALESCE(image_path,'')<>'' OR COALESCE(oss_key,'')<>'')`, jobID)
	return err
}

// ArchivePregen pregen 提货落归档(行已弹出, 用弹出值; created_at=交付时刻)。
func (s *Store) ArchivePregen(p Pregen) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO archive
(job_id, user_id, kind, created_at, width, height, seed, positive, negative, combo, workflow_id, oss_key, image_path)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.JobID, p.UserID, "pregen", time.Now().Format(timeFmt),
		p.Width, p.Height, p.Seed, p.Positive, p.Negative, p.Combo, p.WorkflowID, p.OssKey, p.ImagePath)
	return err
}

// Archive 查单条归档(删除联动/历史取图), 不存在返回 (nil, nil)。
func (s *Store) Archive(jobID string) (*ArchiveRow, error) {
	return scanArchive(s.db.QueryRow(`SELECT `+archiveCols+` FROM archive WHERE job_id=?`, jobID))
}

// ListArchive 用户历史: 纯时间倒序(新→旧; 负责人 2026-11-11 撤掉收藏置顶——
// 有 ?fav=1 只看收藏筛选即可, 收藏置顶会把最新图挤到列表深处); onlyFav 时仅收藏;
// w/h 均 >0 时按出图规格过滤(按设备取回: 客户端上报自己屏幕, 只看本规格);
// offset>0 跳过前 N 条(全部召回翻页用)。
func (s *Store) ListArchive(userID string, onlyFav bool, w, h, offset, limit int) ([]ArchiveRow, error) {
	q := `SELECT ` + archiveCols + ` FROM archive WHERE user_id=?`
	args := []any{userID}
	if onlyFav {
		q += ` AND fav=1`
	}
	if w > 0 && h > 0 {
		q += ` AND width=? AND height=?`
		args = append(args, w, h)
	}
	q += ` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveRow{}
	for rows.Next() {
		var a ArchiveRow
		if err := rows.Scan(&a.JobID, &a.UserID, &a.Kind, &a.CreatedAt, &a.Width, &a.Height,
			&a.Seed, &a.Positive, &a.Negative, &a.Combo, &a.WorkflowID, &a.OssKey, &a.ImagePath, &a.Fav); err != nil {
			return nil, fmt.Errorf("scan archive: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetArchiveFav 收藏/取消收藏。
func (s *Store) SetArchiveFav(jobID string, fav bool) error {
	v := 0
	if fav {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE archive SET fav=? WHERE job_id=?`, v, jobID)
	return err
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
