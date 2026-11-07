package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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
		if err := a.st.RecordSiteEvent("pv", siteVisitorHash(r), siteVisitorIP(r)); err != nil {
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
	if err := a.st.RecordSiteEvent("dl", siteVisitorHash(r), siteVisitorIP(r)); err != nil {
		a.logf("官网计数失败: %v", err)
	}
	http.ServeFile(w, r, fp)
}

// handleGoDL 客户端下载计数中转: 记一次 dl 事件后 302 到当前版本的 Gitee Release 直链。
// 官网按钮与 manifest.url 均指向本端点, 字节仍由 Gitee 承载, 我方只出跳转(语义=发起下载)。
// 客户端下载白名单自 v1.0.0 起即放行 BaseURL 前缀, 全版本兼容; 跨主机跳转时 Go 自动剥离 Authorization。
func (a *API) handleGoDL(w http.ResponseWriter, r *http.Request) {
	if err := a.st.RecordSiteEvent("dl", siteVisitorHash(r), siteVisitorIP(r)); err != nil {
		a.logf("下载计数失败: %v", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, giteeReleaseURL(a.version), http.StatusFound)
}

// giteeReleaseURL 当前版本安装包的 Gitee Release 直链。
func giteeReleaseURL(ver string) string {
	return "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/v" + ver + "/AIWallpaper.exe"
}

// siteVisitorIP 取访客来源 IP(X-Real-IP / X-Forwarded-For / RemoteAddr)。
func siteVisitorIP(r *http.Request) string {
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
	return ip
}

// siteVisitorHash 访客 IP 的匿名哈希(用于 UV 近似去重; 明文 IP 另存 site_events.ip)。
func siteVisitorHash(r *http.Request) string {
	sum := sha1.Sum([]byte("aw-site-uv:" + siteVisitorIP(r)))
	return hex.EncodeToString(sum[:6])
}

// handleGoAPK 安卓 APK 下载计数中转: 记 apk_dl 事件后 302 到 Gitee Release 资产。
// 资产命名约定: tag=android-v{ver}, 文件=AIWallpaper-Android-{ver}.apk(与 giteeAPKURL 对应)。
func (a *API) handleGoAPK(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "releases", "manifest-android.json"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var m releaseManifest
	if json.Unmarshal(raw, &m) != nil || m.Version == "" {
		http.NotFound(w, r)
		return
	}
	if err := a.st.RecordSiteEvent("apk_dl", siteVisitorHash(r), siteVisitorIP(r)); err != nil {
		a.logf("APK 下载计数失败: %v", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, giteeAPKURL(m.Version), http.StatusFound)
}

// giteeAPKURL 当前安卓版本安装包的 Gitee Release 直链。
func giteeAPKURL(ver string) string {
	return "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/android-v" + ver +
		"/AIWallpaper-Android-" + ver + ".apk"
}
