package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wallpaper/internal/policy"
)

// ---------- 运营策略 ----------

// handlePolicy 下发运营策略(活跃时段/备用池目标/暂停选项)。
// 数据源 AW_DATA/policy.json 改文件即生效; 缺失或非法时返回内置默认。
// 策略不含隐私且向导阶段即可拉取, 故不要求鉴权; 客户端失败时回退本地缓存。
func (a *API) handlePolicy(w http.ResponseWriter, r *http.Request) {
	pol := policy.Default()
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "policy.json"))
	if err == nil {
		var p policy.Policy
		if json.Unmarshal(raw, &p) == nil {
			pol = p.Normalize()
		}
	}
	writeOK(w, pol)
}

// ---------- 客户端版本分发 ----------

func (a *API) handleClientLatest(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("version")
	manifestPath := filepath.Join(a.dataDir, "releases", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		// 未发布过版本: 返回同版本(无更新)
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	var m struct {
		Version string `json:"version"`
		File    string `json:"file"`
		SHA256  string `json:"sha256"`
		Size    int64  `json:"size"`
		Notes   string `json:"notes"`
		URL     string `json:"url"` // 可选: 覆盖下载地址(如 Gitee 直链)
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version == "" || m.File == "" {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	if compareVer(m.Version, current) <= 0 {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	if os.Getenv("AW_FORCE_HTTPS") == "1" {
		scheme = "https"
	}
	dlURL := fmt.Sprintf("%s://%s/releases/%s", scheme, r.Host, m.File)
	if m.URL != "" {
		// 下载地址覆盖(如 Gitee 直链): 所有客户端(含旧版)立即改从该地址下载
		dlURL = m.URL
	}
	writeOK(w, map[string]any{
		"version": m.Version,
		"url":     dlURL,
		"sha256":  m.SHA256, "size": m.Size, "notes": m.Notes,
	})
}

func compareVer(a, b string) int {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}
