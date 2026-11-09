package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestAdminOssStatKPI 系统页 OSS 存储 KPI: 未配置显示"—", 配置后走 GetBucketStat。
func TestAdminOssStatKPI(t *testing.T) {
	ts := newTestServer(t, "", false)
	ts.api.adminToken = "adm-test"

	// 未配置 OSS → 卡片显示"—", 面板不因统计失败而500
	t.Setenv("AW_OSS_AK_ID", "")
	r, raw := ts.do(t, "GET", "/admin?token=adm-test", "", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin 应200: %d", r.StatusCode)
	}
	if !strings.Contains(string(raw), "OSS 存储") {
		t.Fatal("系统页应有 OSS 存储卡片")
	}
	if !strings.Contains(string(raw), ">—<") {
		t.Fatal("未配置时应显示—")
	}

	// humanBytes 格式
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1 << 20, "5.0 MB"},
		{int64(3) << 30, "3.00 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Fatalf("humanBytes(%d): got %s want %s", c.n, got, c.want)
		}
	}
}
