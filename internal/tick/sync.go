package tick

import (
	"os"
	"path/filepath"

	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
)

// pullRemoteProfile 双端同步下行: 服务端画像版本更新时才应用(新者胜)。
// 应用前把本地 profile.json 备份为 profile.json.bak(合并策略 A: 新者胜 + 本地备份)。
// 返回 (新画像, 是否应用)。packs 三态(nil=推断 / []=清空)原样保留。
func pullRemoteProfile(dir string, cfg *config.Config, remote *cloud.RemoteProfile) (*config.Profile, bool) {
	if remote == nil || remote.ProfileVersion <= cfg.ProfileVer {
		return nil, false
	}
	src := filepath.Join(dir, "profile.json")
	if b, err := os.ReadFile(src); err == nil {
		_ = os.WriteFile(src+".bak", b, 0o644)
	}
	p := config.NewProfile()
	for k, v := range remote.Profile {
		p.Weights[k] = v
	}
	p.Disliked = append([]string(nil), remote.Disliked...)
	p.CustomKeywords = config.NormalizeKeywords(remote.Custom)
	p.Packs = remote.Packs
	if err := p.Save(dir); err != nil {
		return nil, false
	}
	cfg.ProfileVer = remote.ProfileVersion
	_ = cfg.Save(dir)
	return p, true
}
