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
// 策略不含隐私且客户端首装/检查更新即会拉取, 故不要求鉴权; 客户端失败时回退本地缓存。
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

// releaseManifest releases/manifest*.json 结构(Windows 与安卓共用字段; url 可覆盖下载地址)。
type releaseManifest struct {
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Notes   string `json:"notes"`
	URL     string `json:"url"` // 可选: 覆盖下载地址(如 /go/dl 计数中转)
}

func (a *API) handleClientLatest(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("version")
	// 更新检查是客户端最高频携带自身版本的时机(tick 尾部+手动检查), 顺手上报落库供看板版本分布。
	if current != "" {
		if u := a.authUser(r); u != nil {
			if err := a.st.SetClientVersion(u.UserID, current); err != nil {
				a.logf("客户端版本记录失败 user=%s: %v", u.UserID, err)
			}
		}
	}
	manifestPath := filepath.Join(a.dataDir, "releases", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		// 未发布过版本: 返回同版本(无更新)
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	var m releaseManifest
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

// handleAndroidLatest GET /api/v1/android/latest 安卓客户端检查更新。
// 数据源 AW_DATA/releases/manifest-android.json(version/sha256/size/url),
// url 统一指 /go/apk 计数中转; 未发布过 APK 时返回同版本(无更新)。
func (a *API) handleAndroidLatest(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("version")
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "releases", "manifest-android.json"))
	if err != nil {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	var m releaseManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Version == "" || m.URL == "" {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	if compareVer(m.Version, current) <= 0 {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	writeOK(w, map[string]any{
		"version": m.Version,
		"url":     m.URL,
		"sha256":  m.SHA256, "size": m.Size, "notes": m.Notes,
	})
}

// handleAgentLatest GET /api/v1/agent/latest 出图机代理检查更新(S3 版本管理)。
// 数据源 AW_DATA/releases/manifest-agent.json; url 统一指 /go/agent 计数中转;
// 未发布过代理版本时返回同版本(无更新)。
func (a *API) handleAgentLatest(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("version")
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "releases", "manifest-agent.json"))
	if err != nil {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	var m releaseManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Version == "" || m.URL == "" {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	if compareVer(m.Version, current) <= 0 {
		writeOK(w, map[string]any{"version": current, "notes": ""})
		return
	}
	writeOK(w, map[string]any{
		"version": m.Version,
		"url":     m.URL,
		"sha256":  m.SHA256, "size": m.Size, "notes": m.Notes,
	})
}
