package store

import "testing"

// TestPregenTableMigrationFromV1 复现线上场景: 旧库 pregen 表(主键 user_id, 无 created_at 列)
// 打开时应被安全重建为新结构(主键 job_id), 且后续多单写入正常。
// 版本化迁移下, 真正的旧生产库不含 schema_version 表 → 视为版本 0, 顺序重放全部迁移;
// 故此处一并删除 schema_version, 忠实模拟早于版本化迁移的线上库。
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
	// 删除版本表, 模拟早于版本化迁移的线上旧库(重开时 cur=0 → 全部迁移重放)
	if _, err := s.db.Exec(`DROP TABLE schema_version`); err != nil {
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

// TestProfileSnapshotPacksRoundTrip 画像快照 packs 字段三态往返:
// ""(旧快照无值)/"[]"(显式清空)/'["写实美女"]'(选中) 必须原样回读,
// 否则服务端主动预生成会丢包(乱出非偏好题材)。
func TestProfileSnapshotPacksRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cases := []string{"", "[]", `["写实美女","自然山河"]`}
	for _, packs := range cases {
		if err := s.SaveProfileSnapshot(ProfileSnapshot{
			UserID: "u_t", ProfileVersion: 3, Profile: `{"style/realism":0.9}`,
			Packs: packs, ScreenW: 1920, ScreenH: 1080,
		}); err != nil {
			t.Fatalf("save packs=%q: %v", packs, err)
		}
		got, err := s.LoadProfileSnapshot("u_t")
		if err != nil || got == nil {
			t.Fatalf("load packs=%q: %v", packs, err)
		}
		if got.Packs != packs {
			t.Errorf("packs round trip = %q, want %q", got.Packs, packs)
		}
		if got.ProfileVersion != 3 || got.ScreenW != 1920 {
			t.Errorf("other fields mangled: %+v", got)
		}
	}
}

// TestM008DeviceDedupAndAttrs 迁移 v8: 存量重复设备行先去重再建唯一索引(索引先于去重会崩),
// 旧设备回填 type=pc, AddDevice 幂等, 属性 upsert 空值不覆盖, 屏幕/类型查询正确。
func TestM008DeviceDedupAndAttrs(t *testing.T) {
	path := t.TempDir() + "/m8.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 撤索引+删版本号, 忠实模拟"有重复行的未迁移旧库"
	if _, err := s.db.Exec(`DROP INDEX idx_devices_du`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO devices(device,user_id,created_at)
VALUES('h1','u1','2026-01-01T00:00:00Z'),('h1','u1','2026-01-02T00:00:00Z'),('h2','u1','2026-01-03T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path) // 重放全部迁移 → v8
	if err != nil {
		t.Fatalf("迁移重放失败: %v", err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("去重后应 2 行, got %d", n)
	}
	var typ string
	if err := s2.db.QueryRow(`SELECT device_type FROM devices WHERE device='h1'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "pc" {
		t.Fatalf("存量设备应回填 pc, got %q", typ)
	}
	// AddDevice 幂等(唯一索引 + upsert)
	if err := s2.AddDevice("h1", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE device='h1' AND user_id='u1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("AddDevice 应幂等, got %d", n)
	}
	// 属性 upsert: 首次写入 + 空值不覆盖
	if err := s2.UpsertDeviceAttrs(DeviceAttr{
		Device: "a1", UserID: "u1", Type: "android", Model: "Xiaomi 14",
		OS: "Android", OSVersion: "14", AppVer: "1.0.0", ScreenW: 1080, ScreenH: 2400,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s2.UpsertDeviceAttrs(DeviceAttr{Device: "a1", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s2.AdminDeviceRows(10)
	if err != nil {
		t.Fatal(err)
	}
	var a1 *AdminDeviceRow
	for i := range rows {
		if rows[i].Device == "a1" {
			a1 = &rows[i]
		}
	}
	if a1 == nil || a1.Type != "android" || a1.Model != "Xiaomi 14" || a1.ScreenW != 1080 {
		t.Fatalf("空值覆盖了属性: %+v", a1)
	}
	// 类型判定与 PC 屏幕查询
	if ok, err := s2.DeviceIsAndroid("a1", "u1"); err != nil || !ok {
		t.Fatalf("DeviceIsAndroid: %v %v", ok, err)
	}
	if ok, _ := s2.DeviceIsAndroid("h1", "u1"); ok {
		t.Fatal("pc 设备不应判为安卓")
	}
	if w, h, _ := s2.RecentPCScreen("u1"); w != 0 || h != 0 {
		t.Fatalf("尚无带屏幕的 PC 设备: %dx%d", w, h)
	}
	if err := s2.UpsertDeviceAttrs(DeviceAttr{Device: "pc1", UserID: "u1", Type: "pc", ScreenW: 2240, ScreenH: 1400}); err != nil {
		t.Fatal(err)
	}
	if w, h, _ := s2.RecentPCScreen("u1"); w != 2240 || h != 1400 {
		t.Fatalf("RecentPCScreen: %dx%d", w, h)
	}
	// TouchDeviceScreen
	if err := s2.TouchDeviceScreen("a1", "u1", 1080, 2400); err != nil {
		t.Fatal(err)
	}
	if err := s2.TouchDeviceScreen("ghost", "u1", 1, 1); err != nil {
		t.Fatalf("未登记设备 touch 不应报错: %v", err)
	}
}
