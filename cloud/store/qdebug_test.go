package store

import (
	"testing"
)

func TestQDebugPop(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertPregen(Pregen{UserID: "u1", ProfileVersion: 2, JobID: "j1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPregenReady("j1", "/tmp/x.png"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.PeekPregenReady("u1", 2); err != nil || p == nil {
		t.Fatalf("peek: %+v %v", p, err)
	}
	p, err := s.PopPregenReady("u1")
	t.Logf("pop=%+v err=%v", p, err)
	if p == nil {
		var cnt int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM pregen`).Scan(&cnt)
		t.Fatalf("pop nil; rows=%d", cnt)
	}
}

func TestQDebugSubquery(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.InsertPregen(Pregen{UserID: "u1", ProfileVersion: 2, JobID: "j1"})
	_ = s.SetPregenReady("j1", "/x.png")
	var n int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM pregen WHERE user_id=? AND ready=1 AND profile_version=(SELECT COALESCE(MAX(profile_version),0) FROM pregen p2 WHERE p2.user_id=? AND p2.ready=1)`, "u1", "u1").Scan(&n)
	t.Logf("subquery count=%d err=%v", n, err)
	// 排查: ready 列与 image_path 实际值
	var ready int
	var img string
	_ = s.db.QueryRow(`SELECT ready, COALESCE(image_path,'') FROM pregen WHERE job_id='j1'`).Scan(&ready, &img)
	t.Logf("row ready=%d image=%q", ready, img)
}
