package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ---------- 本地收藏清单 ----------
//
// 旧时间戳命名的留档文件没有 jobId, 无法关联云端 archive 收藏(安卓端同款限制),
// 用本地清单兜底标记; {jobID}.png 文件的收藏仍以云端为准(跨端同步)。
// 按"文件名"为键 —— 文件名即身份, 不拼进任何路径, 无穿越风险。

func localFavsPath(dir string) string { return filepath.Join(dir, "favs.json") }

// LoadLocalFavs 读本地收藏清单(文件名 → true); 无文件时返回空表。
func LoadLocalFavs(dir string) (map[string]bool, error) {
	m := map[string]bool{}
	b, err := os.ReadFile(localFavsPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return m, err
	}
	var raw map[string]bool
	if err := json.Unmarshal(b, &raw); err != nil {
		return m, err
	}
	for k, v := range raw {
		if v {
			m[k] = true
		}
	}
	return m, nil
}

// SetLocalFav 写/删一条本地收藏标记。
func SetLocalFav(dir, name string, fav bool) error {
	m, err := LoadLocalFavs(dir)
	if err != nil {
		m = map[string]bool{}
	}
	if fav {
		m[name] = true
	} else {
		delete(m, name)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(localFavsPath(dir), b, 0o644)
}
