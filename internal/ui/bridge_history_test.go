package ui

import (
	"testing"

	"wallpaper/internal/config"
	"wallpaper/internal/store"
)

// TestGetCloudHistoryCCReady makeBackend 必须产出就绪的云客户端(否则历史/配对全废)。
func TestGetCloudHistoryCCReady(t *testing.T) {
	dir := t.TempDir()
	if err := config.EnsureSubdirs(dir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Initialized = true
	cfg.UserID = "u_test"
	cfg.Token = "tok"
	cfg.CloudURL = "https://example.com"
	b := &Bridge{dir: dir, st: st, cfg: cfg}
	b.be, b.cc = makeBackend(cfg)
	if b.cc == nil {
		t.Fatal("makeBackend returned nil cloud client in cloud mode")
	}
	// 不联网: 只验证 cc 就绪判定分支(错误应是网络错误, 不是直连模式文案)
	r := b.GetCloudHistory(0)
	if r.Message == "当前为直连调试模式, 无云端历史。" {
		t.Fatalf("cc is nil at call time: %+v", r)
	}
	t.Logf("msg=%q ok=%v items=%d", r.Message, r.OK, len(r.Items))
}

// TestIsJobIDFile P6: 16hex(新)与24hex(存量)都认, 其余长度/非hex拒绝(与安卓 jobIdOf 同规则)。
func TestIsJobIDFile(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0123456789abcdef", true},               // 16hex = P6 新任务ID
		{"0123456789abcdef01234567", true},       // 24hex = 存量
		{"0123456789abcdef0", false},             // 17hex
		{"0123456789abcde", false},               // 15hex
		{"0123456789abcdeg", false},              // 非hex
		{"20261111143406", false},                // 14位(旧时间戳主体, 真实旧名还带 wallpaper_ 前缀)
		{"wallpaper_20261111", false},            // 旧命名
	}
	for _, c := range cases {
		if got := isJobIDFile(c.in); got != c.want {
			t.Errorf("isJobIDFile(%q)=%v want %v", c.in, got, c.want)
		}
	}
}
