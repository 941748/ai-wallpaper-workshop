package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wallpaper/cloud/store"
)

func setOSSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AW_OSS_AK_ID", "testak")
	t.Setenv("AW_OSS_AK_SECRET", "testsecret")
	t.Setenv("AW_OSS_BUCKET", "bkt")
	t.Setenv("AW_OSS_ENDPOINT", "oss-cn-hangzhou.aliyuncs.com")
}

// TestPresignOSSGETGolden 预签名 URL 金标准(算法已对真实桶实证, 此处锁死不回退)。
func TestPresignOSSGETGolden(t *testing.T) {
	setOSSEnv(t)
	got, err := presignOSSGETAt("images/a b.jpg", 1794000000)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://bkt.oss-cn-hangzhou.aliyuncs.com/images/a%20b.jpg" +
		"?OSSAccessKeyId=testak&Expires=1794000000&Signature=eBUnxtNmEq%2FHoXduUR4X7pYxXeM%3D"
	if got != want {
		t.Fatalf("presign mismatch:\n got %s\nwant %s", got, want)
	}

	// 未配置 → 明确报错(端点据此回退本地)
	t.Setenv("AW_OSS_AK_ID", "")
	if _, err := presignOSSGETAt("k", 1); err == nil {
		t.Fatal("未配置应报错")
	}
}

// noRedirectClient 只取首跳响应(302 不真跳, 避免测试打真实外网)。
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestJobImageOssDualMode 任务图端点双模: oss_key → 302 签名直下; 仅本地 → 200 文件。
func TestJobImageOssDualMode(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)

	// 分支1: OSS 模式
	if err := ts.st.CreateJob(store.Job{JobID: "joboss1", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("joboss1", "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobOssKey("joboss1", "images/x.jpg"); err != nil {
		t.Fatal(err)
	}
	setOSSEnv(t)
	req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/v1/generate/joboss1/image", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("OSS 分支应302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "https://bkt.oss-cn-hangzhou.aliyuncs.com/images/x.jpg?") ||
		!strings.Contains(loc, "Signature=") {
		t.Fatalf("Location 异常: %s", loc)
	}

	// 分支2: 本地回退(OSS 未配置)
	t.Setenv("AW_OSS_AK_ID", "")
	img := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(img, []byte("fakejpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.CreateJob(store.Job{JobID: "joblocal1", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("joblocal1", "done", "", img); err != nil {
		t.Fatal(err)
	}
	_, raw := ts.do(t, "GET", "/api/v1/generate/joblocal1/image", tok, nil)
	if string(raw) != "fakejpg" {
		t.Fatalf("本地分支应返回文件内容, got %q", raw)
	}
}

// TestPregenImageOssRedirect 预生成图: oss_key → 302(提货即删语义不变)。
func TestPregenImageOssRedirect(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)
	if err := ts.st.InsertPregen(store.Pregen{
		JobID: "pg-test1", UserID: uid, ProfileVersion: 1,
		Positive: "pos", Negative: "neg", WorkflowID: "wf",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetPregenReady("pg-test1", ""); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetPregenOssKey("pg-test1", "pregen/p.jpg"); err != nil {
		t.Fatal(err)
	}
	setOSSEnv(t)
	req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/v1/pregen/image?job_id=pg-test1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("pregen OSS 分支应302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "/pregen/p.jpg?") {
		t.Fatalf("Location 异常: %s", loc)
	}
	// 提货即归档: 弹出的 pregen 成品进入用户历史
	if arc, _ := ts.st.Archive("pg-test1"); arc == nil || arc.OssKey != "pregen/p.jpg" {
		t.Fatalf("提货应落归档: %+v", arc)
	}
}

// TestOssKeyForUserScoped S2 按用户重键金标准。
func TestOssKeyForUserScoped(t *testing.T) {
	got := store.OssKeyFor("u_bb85xx", "j-abc123")
	want := "u/u_bb85xx/j-abc123.jpg"
	if got != want {
		t.Fatalf("OssKeyFor: got %s want %s", got, want)
	}
}

// TestImageDelete S2b 删除联动: 本地清理+幂等+越权404+OSS清失败不改行。
func TestImageDelete(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)

	// 第二用户(不同设备 → 不同用户)
	resp, err := http.Post(ts.srv.URL+"/api/v1/users/register", "application/json",
		strings.NewReader(`{"device":"other-phone"}`))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			UserID string `json:"user_id"`
			Token  string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !env.Success || env.Data.UserID == uid {
		t.Fatalf("第二用户注册失败: %+v", env)
	}
	tok2 := env.Data.Token

	// 本地文件删除 + 行引用清空
	img := filepath.Join(ts.data, "images", "del1.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.CreateJob(store.Job{JobID: "jobdel1", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("jobdel1", "done", "", img); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.ArchiveJob("jobdel1"); err != nil {
		t.Fatal(err)
	}
	r, raw := ts.do(t, "POST", "/api/v1/images/delete", tok, map[string]string{"job_id": "jobdel1"})
	if r.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"success":true`) {
		t.Fatalf("删除应200: %d %s", r.StatusCode, raw)
	}
	if _, err := os.Stat(img); !os.IsNotExist(err) {
		t.Fatal("本地文件应已删除")
	}
	if j, _ := ts.st.Job("jobdel1"); j == nil || j.ImagePath != "" || j.OssKey != "" {
		t.Fatalf("行引用应已清空: %+v", j)
	}
	if arc, _ := ts.st.Archive("jobdel1"); arc != nil {
		t.Fatalf("归档行应已删除: %+v", arc)
	}

	// 幂等: 再删一次仍200
	r, _ = ts.do(t, "POST", "/api/v1/images/delete", tok, map[string]string{"job_id": "jobdel1"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("幂等删除应200, got %d", r.StatusCode)
	}

	// 越权: 他人任务404 且不改行
	img2 := filepath.Join(ts.data, "images", "del2.jpg")
	if err := os.WriteFile(img2, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.CreateJob(store.Job{JobID: "jobdel2", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("jobdel2", "done", "", img2); err != nil {
		t.Fatal(err)
	}
	r, _ = ts.do(t, "POST", "/api/v1/images/delete", tok2, map[string]string{"job_id": "jobdel2"})
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("越权应404, got %d", r.StatusCode)
	}
	if _, err := os.Stat(img2); err != nil {
		t.Fatal("越权不应删文件")
	}

	// OSS 清理失败(未配置) → 500 且不改行(客户端据此降级只删本地)
	if err := ts.st.CreateJob(store.Job{JobID: "jobdel3", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("jobdel3", "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobOssKey("jobdel3", "u/u_x/jobdel3.jpg"); err != nil {
		t.Fatal(err)
	}
	r, _ = ts.do(t, "POST", "/api/v1/images/delete", tok, map[string]string{"job_id": "jobdel3"})
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("OSS 清失败应500, got %d", r.StatusCode)
	}
	if j, _ := ts.st.Job("jobdel3"); j == nil || j.OssKey != "u/u_x/jobdel3.jpg" {
		t.Fatalf("失败不应改行: %+v", j)
	}
}

// TestHistory S2c: 归档历史独立于 jobs(7 天 Purge 后仍可取图)+越权隔离。
func TestHistory(t *testing.T) {
	ts := newTestServer(t, "", false)
	uid, tok := ts.register(t)

	img := filepath.Join(ts.data, "images", "h1.jpg")
	if err := os.WriteFile(img, []byte("hist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.CreateJob(store.Job{JobID: "jobh1", UserID: uid, Kind: "gen", Priority: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.SetJobStatus("jobh1", "done", "", img); err != nil {
		t.Fatal(err)
	}
	if err := ts.st.ArchiveJob("jobh1"); err != nil {
		t.Fatal(err)
	}

	// 列表含该条, image_url 指向历史取图端点
	r, raw := ts.do(t, "GET", "/api/v1/history", tok, nil)
	if r.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"job_id":"jobh1"`) {
		t.Fatalf("history 应含 jobh1: %d %s", r.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "/api/v1/history/jobh1/image") {
		t.Fatalf("image_url 应为历史端点: %s", raw)
	}

	// 取图 200
	r, raw = ts.do(t, "GET", "/api/v1/history/jobh1/image", tok, nil)
	if r.StatusCode != http.StatusOK || string(raw) != "hist" {
		t.Fatalf("历史取图应200: %d %q", r.StatusCode, raw)
	}

	// jobs 被 7 天 Purge 后: 历史仍可用, generate 端点已失效
	if err := ts.st.PurgeJobs(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ = ts.do(t, "GET", "/api/v1/history/jobh1/image", tok, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("purge 后历史取图应200, got %d", r.StatusCode)
	}
	r, _ = ts.do(t, "GET", "/api/v1/generate/jobh1/image", tok, nil)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("purge 后 generate 端点应404, got %d", r.StatusCode)
	}

	// 越权: 他人列表不含、取图404
	resp, err := http.Post(ts.srv.URL+"/api/v1/users/register", "application/json",
		strings.NewReader(`{"device":"other-tablet"}`))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !env.Success {
		t.Fatal("第二用户注册失败")
	}
	r, raw = ts.do(t, "GET", "/api/v1/history", env.Data.Token, nil)
	if strings.Contains(string(raw), "jobh1") {
		t.Fatalf("他人历史不应含 jobh1: %s", raw)
	}
	r, _ = ts.do(t, "GET", "/api/v1/history/jobh1/image", env.Data.Token, nil)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("越权取图应404, got %d", r.StatusCode)
	}
}
