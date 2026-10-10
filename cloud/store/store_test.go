package store

import "testing"

// TestPregenTableMigrationFromV1 复现线上场景: 旧库 pregen 表(主键 user_id, 无 created_at 列)
// 打开时应被安全重建为新结构(主键 job_id), 且后续多单写入正常。
func TestPregenTableMigrationFromV1(t *testing.T) {
	path := t.TempDir() + "/m.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 构造旧结构: 替换为旧版 DDL(照抄线上旧表)
	if _, err := s.db.Exec(`DROP TABLE pregen`); err != nil {
		t.Fatal(err)
	}
	oldDDL := `CREATE TABLE pregen (
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
)`
	if _, err := s.db.Exec(oldDDL); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// 重开: 迁移应自动完成且不报错(旧代码此处因索引引用 created_at 导致崩溃)
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after old schema: %v", err)
	}
	defer s2.Close()
	var pk string
	if err := s2.db.QueryRow(`SELECT name FROM pragma_table_info('pregen') WHERE pk>0 LIMIT 1`).Scan(&pk); err != nil || pk != "job_id" {
		t.Fatalf("pk=%q err=%v, want job_id", pk, err)
	}
	// 迁移后多单写入正常
	if err := s2.InsertPregen(Pregen{UserID: "u1", ProfileVersion: 1, JobID: "j1"}); err != nil {
		t.Fatal(err)
	}
	if err := s2.InsertPregen(Pregen{UserID: "u1", ProfileVersion: 1, JobID: "j2"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.CountPregens("u1"); n != 2 {
		t.Fatalf("count=%d want 2", n)
	}
}
