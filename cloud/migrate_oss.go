package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"wallpaper/cloud/store"
)

// ---------- S2a 存量迁移: 服务器本地图片 → OSS(按用户重键 u/{uid}/{jobID}.jpg) ----------
//
// Phase1 本地文件上传(Content-MD5 由 OSS 校验, 通过=完整性保证) → 打 oss_key(本地文件保留作回退备份)
// Phase2 已在 OSS 的旧键对象 → 服务端 CopyObject 重键 → 更新行 → 删旧对象
// Phase3 孤儿本地文件(库里无引用) → 删除(5 分钟内新文件跳过, 避开写入竞态)
// 触发: GET /admin/migrate-oss?token=… (异步执行, journal 看进度; 并发防重入)

var migrateOSSRunning atomic.Bool

// handleMigrateOSS 触发后台迁移(幂等: 运行中返回 running)。
func (a *API) handleMigrateOSS(w http.ResponseWriter, r *http.Request) {
	if a.adminToken == "" || r.URL.Query().Get("token") != a.adminToken {
		http.NotFound(w, r)
		return
	}
	if !migrateOSSRunning.CompareAndSwap(false, true) {
		writeOK(w, map[string]bool{"running": true})
		return
	}
	go a.migrateOSS()
	writeOK(w, map[string]bool{"started": true})
}

func (a *API) migrateOSS() {
	defer migrateOSSRunning.Store(false)
	if !ossConfigured() {
		a.logf("OSS 迁移中止: OSS 未配置")
		return
	}
	start := time.Now()
	var upN, rekeyN, orphanN, errN int
	var upBytes int64

	// Phase1: 本地文件 → OSS(新键)
	jobs, err := a.st.LocalUnmigratedJobs()
	if err != nil {
		a.logf("OSS 迁移查询失败: %v", err)
		return
	}
	for _, j := range jobs {
		data, err := os.ReadFile(j.ImagePath)
		if err != nil {
			a.logf("OSS 迁移跳过(读失败) job=%s: %v", j.JobID, err)
			errN++
			continue
		}
		key := store.OssKeyFor(j.UserID, j.JobID, j.Width, j.Height)
		t0 := time.Now()
		if err := ossPutObject(key, data, "image/jpeg"); err != nil {
			a.logf("OSS 迁移上传失败 job=%s: %v", j.JobID, err)
			errN++
			continue
		}
		_ = a.st.SetJobOssKey(j.JobID, key)
		if j.Kind == "pregen" {
			_ = a.st.SetPregenOssKey(j.JobID, key)
		}
		upN++
		upBytes += int64(len(data))
		a.logf("OSS 迁移上传 job=%s -> %s (%dB, %s)", j.JobID, key, len(data), time.Since(t0).Round(time.Millisecond))
	}
	// Phase1b: pregen 行补迁(job 行已迁但行缺 key 的兜底)
	pregens, err := a.st.LocalUnmigratedPregens()
	if err == nil {
		for _, p := range pregens {
			data, err := os.ReadFile(p.ImagePath)
			if err != nil {
				continue
			}
			key := store.OssKeyFor(p.UserID, p.JobID, p.Width, p.Height)
			if err := ossPutObject(key, data, "image/jpeg"); err != nil {
				errN++
				continue
			}
			_ = a.st.SetPregenOssKey(p.JobID, key)
			_ = a.st.SetJobOssKey(p.JobID, key)
			upN++
			upBytes += int64(len(data))
		}
	}

	// Phase2: 旧键 → 用户前缀新键(服务端拷贝, 不经本地字节)
	rekeys, err := a.st.OssRekeyCandidates()
	if err != nil {
		a.logf("OSS 重键查询失败: %v", err)
		return
	}
	for _, j := range rekeys {
		newKey := store.OssKeyFor(j.UserID, j.JobID, j.Width, j.Height)
		if newKey == j.OssKey {
			continue
		}
		if err := ossCopyObject(j.OssKey, newKey); err != nil {
			a.logf("OSS 重键失败 job=%s: %v", j.JobID, err)
			errN++
			continue
		}
		_ = a.st.SetJobOssKey(j.JobID, newKey)
		if j.Kind == "pregen" {
			_ = a.st.SetPregenOssKey(j.JobID, newKey)
		}
		_ = ossDeleteObject(j.OssKey)
		rekeyN++
		a.logf("OSS 重键 job=%s: %s -> %s", j.JobID, j.OssKey, newKey)
	}
	pgRekeys, err := a.st.OssRekeyPregens()
	if err == nil {
		for _, p := range pgRekeys {
			newKey := store.OssKeyFor(p.UserID, p.JobID, p.Width, p.Height)
			if newKey == p.OssKey {
				continue
			}
			if err := ossCopyObject(p.OssKey, newKey); err != nil {
				errN++
				continue
			}
			_ = a.st.SetPregenOssKey(p.JobID, newKey)
			_ = ossDeleteObject(p.OssKey)
			rekeyN++
		}
	}

	// Phase3: 孤儿本地文件(库里无引用)删除; 5 分钟内新文件跳过(避开写入竞态)
	refs, err := a.st.ReferencedImagePaths()
	if err == nil {
		cutoff := time.Now().Add(-5 * time.Minute)
		for _, dir := range []string{"images", "pregen"} {
			entries, err := os.ReadDir(filepath.Join(a.dataDir, dir))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				full := filepath.Join(a.dataDir, dir, e.Name())
				if refs[full] {
					continue
				}
				if fi, err := e.Info(); err == nil && fi.ModTime().After(cutoff) {
					continue
				}
				if err := os.Remove(full); err == nil {
					orphanN++
					a.logf("OSS 迁移清理孤儿文件: %s", full)
				}
			}
		}
	}

	a.logf("OSS 迁移完成: 上传%d个(%dB) 重键%d个 孤儿删除%d个 失败%d 耗时%s",
		upN, upBytes, rekeyN, orphanN, errN, time.Since(start).Round(time.Second))
}
