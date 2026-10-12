package main

import (
	"crypto/sha1"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ---------- 官网 ----------

// handleSite 官网静态页(index.html 与 assets/); 非站点路径返回 404。
func (a *API) handleSite(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") {
		writeFail(w, http.StatusNotFound, "接口不存在")
		return
	}
	if p == "/" {
		p = "/index.html"
	}
	site, err := filepath.Abs(a.siteDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fp, err := filepath.Abs(filepath.Join(site, filepath.Clean(p)))
	if err != nil || !strings.HasPrefix(fp, site+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	if p == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		if err := a.st.RecordSiteEvent("pv", siteVisitorHash(r)); err != nil {
			a.logf("官网计数失败: %v", err)
		}
	}
	if strings.HasPrefix(p, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	http.ServeFile(w, r, fp)
}

// handleDownload 客户端安装包下载(支持 Range 断点续传)。
func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	fp := filepath.Join(a.dlDir, "AIWallpaper.exe")
	if _, err := os.Stat(fp); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="AIWallpaper-Setup.exe"`)
	if err := a.st.RecordSiteEvent("dl", siteVisitorHash(r)); err != nil {
		a.logf("官网计数失败: %v", err)
	}
	http.ServeFile(w, r, fp)
}

// siteVisitorHash 访客 IP 的匿名哈希(仅用于 UV 近似去重, 不存明文 IP)。
func siteVisitorHash(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip == "" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip = strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	if ip == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			ip = host
		} else {
			ip = r.RemoteAddr
		}
	}
	sum := sha1.Sum([]byte("aw-site-uv:" + ip))
	return hex.EncodeToString(sum[:6])
}
