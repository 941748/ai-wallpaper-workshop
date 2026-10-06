package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wallpaper/internal/cloud"
)

// mockCloud 启动 mock 云端: /api/v1/client/latest 返回 meta(base),
// /releases/ 返回 payload 字节。
func mockCloud(t *testing.T, meta func(base string) map[string]any, payload []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/client/latest", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"success": true, "data": meta(srv.URL)})
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/releases/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func stagedPath(dir string) string { return filepath.Join(dir, "bin", "wallpaper.new.exe") }

func assertNotStaged(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(stagedPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("新版本不应被暂存: %s", stagedPath(dir))
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.1.0", 1},
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"v0.10.0", "0.9.9", 1},
		{"1.0", "1.0.1", -1},
		{"", "0.1.0", -1},
		{"1.2.3", "1.2.3-beta", 0},
		{"2.0.0", "10.0.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%q,%q)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckNoUpdateForSameOrOlder(t *testing.T) {
	payload := []byte("v0.2.0")
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "wallpaper.exe")

	srvSame := mockCloud(t, func(base string) map[string]any {
		return map[string]any{"version": "0.1.0", "url": base + "/releases/w.exe",
			"sha256": sumHex(payload), "size": int64(len(payload))}
	}, payload)
	c := cloud.New(srvSame.URL, "u1", "tok", "dev")
	if Check(context.Background(), c, dir, exe, func(string, ...any) {}) {
		t.Fatal("同版本不应触发更新")
	}

	srvOld := mockCloud(t, func(base string) map[string]any {
		return map[string]any{"version": "0.0.9", "url": base + "/releases/w.exe",
			"sha256": sumHex(payload), "size": int64(len(payload))}
	}, payload)
	c2 := cloud.New(srvOld.URL, "u1", "tok", "dev")
	if Check(context.Background(), c2, dir, exe, func(string, ...any) {}) {
		t.Fatal("旧版本不应触发更新")
	}
	assertNotStaged(t, dir)
}

func TestCheckRejectsBadSHA(t *testing.T) {
	payload := []byte("payload-v2")
	srv := mockCloud(t, func(base string) map[string]any {
		return map[string]any{"version": "0.2.0", "url": base + "/releases/w.exe",
			"sha256": strings.Repeat("ab", 32), "size": int64(len(payload))}
	}, payload)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := cloud.New(srv.URL, "u1", "tok", "dev")
	if Check(context.Background(), c, dir, filepath.Join(dir, "bin", "wallpaper.exe"), func(string, ...any) {}) {
		t.Fatal("sha256 校验失败必须放弃更新")
	}
	assertNotStaged(t, dir)
}

func TestCheckRejectsSizeMismatch(t *testing.T) {
	payload := []byte("payload-v2")
	srv := mockCloud(t, func(base string) map[string]any {
		return map[string]any{"version": "0.2.0", "url": base + "/releases/w.exe",
			"sha256": sumHex(payload), "size": int64(len(payload) + 10)}
	}, payload)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := cloud.New(srv.URL, "u1", "tok", "dev")
	if Check(context.Background(), c, dir, filepath.Join(dir, "bin", "wallpaper.exe"), func(string, ...any) {}) {
		t.Fatal("大小不符必须放弃更新")
	}
	assertNotStaged(t, dir)
}

func TestCheckStagesNewVersion(t *testing.T) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		t.Skip("需要 Windows 环境")
	}
	spawnable := filepath.Join(root, "System32", "cmd.exe") // 仅用于让助手进程可启动
	if _, err := os.Stat(spawnable); err != nil {
		t.Skip("cmd.exe 不可用")
	}

	payload := []byte("wallpaper-v2-binary-content")
	srv := mockCloud(t, func(base string) map[string]any {
		return map[string]any{"version": "0.2.0", "url": base + "/releases/wallpaper-0.2.0.exe",
			"sha256": sumHex(payload), "size": int64(len(payload))}
	}, payload)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := cloud.New(srv.URL, "u1", "tok", "dev")
	if !Check(context.Background(), c, dir, spawnable, func(string, ...any) {}) {
		t.Fatal("合法新版本应暂存成功")
	}
	got, err := os.ReadFile(stagedPath(dir))
	if err != nil {
		t.Fatalf("新版本未暂存: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("暂存内容与下载不一致")
	}
}

func TestApplyUpdateSwap(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, "wallpaper.exe")
	newFile := filepath.Join(bin, "wallpaper.new.exe")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdate(target, newFile); err != nil {
		t.Fatalf("换装失败: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "NEW" {
		t.Fatalf("目标内容 = %q, %v", got, err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatal("暂存文件应被移走")
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatal("旧版 .old 应被清理")
	}
}

func TestApplyUpdateRollbackWhenNewMissing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, "wallpaper.exe")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ApplyUpdate(target, filepath.Join(bin, "missing.exe"))
	if err == nil {
		t.Fatal("新文件缺失时必须报错")
	}
	// 回滚: 旧版必须仍可运行
	got, rerr := os.ReadFile(target)
	if rerr != nil || string(got) != "OLD" {
		t.Fatalf("回滚失败, 目标内容 = %q, %v", got, rerr)
	}
	if _, serr := os.Stat(target + ".old"); !os.IsNotExist(serr) {
		t.Fatal("回滚后不应残留 .old")
	}
}

func TestCleanupOldFiles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wallpaper.exe.old", "a.old", "keep.exe"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	CleanupOldFiles(filepath.Dir(bin))
	for _, name := range []string{"wallpaper.exe.old", "a.old"} {
		if _, err := os.Stat(filepath.Join(bin, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被清理", name)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "keep.exe")); err != nil {
		t.Fatal("非 .old 文件不应被清理")
	}
}
