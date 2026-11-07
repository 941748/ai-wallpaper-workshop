package tick

import (
	"os"
	"path/filepath"
	"testing"

	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
)

// TestPullRemoteProfile 双端同步下行: 新者胜、旧档备份、packs 三态保留、落后版本不应用。
func TestPullRemoteProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ProfileVer: 3}

	// 1) 服务端更新(pv4) → 应用
	remote := &cloud.RemoteProfile{
		ProfileVersion: 4,
		Profile:        map[string]float64{"style/realism": 0.9},
		Disliked:       []string{"subject/weapons"},
		Custom:         []string{"猫咪"},
		Packs:          []string{"写实美女"},
	}
	p, ok := pullRemoteProfile(dir, cfg, remote)
	if !ok {
		t.Fatal("应应用更新")
	}
	if cfg.ProfileVer != 4 {
		t.Fatalf("cfg pv: %d", cfg.ProfileVer)
	}
	if p.Weights["style/realism"] != 0.9 || len(p.Disliked) != 1 || len(p.CustomKeywords) != 1 {
		t.Fatalf("内容: %+v", p)
	}
	if len(p.Packs) != 1 || p.Packs[0] != "写实美女" {
		t.Fatalf("packs: %v", p.Packs)
	}
	// 落盘可读回
	back, err := config.LoadProfile(dir)
	if err != nil || back.Weights["style/realism"] != 0.9 {
		t.Fatalf("落盘读回: %v %+v", err, back)
	}
	// 首次无本地文件 → 无备份
	if _, err := os.Stat(filepath.Join(dir, "profile.json.bak")); !os.IsNotExist(err) {
		t.Fatal("首拉不应有备份")
	}

	// 2) 同版本 → 不应用
	if _, ok := pullRemoteProfile(dir, cfg, &cloud.RemoteProfile{ProfileVersion: 4}); ok {
		t.Fatal("同版本不应应用")
	}
	// 3) 落后版本 → 不应用
	if _, ok := pullRemoteProfile(dir, cfg, &cloud.RemoteProfile{ProfileVersion: 2}); ok {
		t.Fatal("落后版本不应应用")
	}

	// 4) 再更新(pv5, packs=null 推断态) → 应用且产生备份
	remote2 := &cloud.RemoteProfile{
		ProfileVersion: 5,
		Profile:        map[string]float64{"subject/cat": 0.9},
		Packs:          nil,
	}
	p2, ok := pullRemoteProfile(dir, cfg, remote2)
	if !ok || cfg.ProfileVer != 5 {
		t.Fatalf("二拉: ok=%v pv=%d", ok, cfg.ProfileVer)
	}
	if p2.Packs != nil {
		t.Fatalf("packs null 应保留推断态: %v", p2.Packs)
	}
	bak, err := os.ReadFile(filepath.Join(dir, "profile.json.bak"))
	if err != nil {
		t.Fatal("应有备份")
	}
	if string(bak) == "" || !contains(string(bak), "style/realism") {
		t.Fatal("备份应为上一版内容")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
