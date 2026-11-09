package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wallpaper/internal/desktop"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
)

// ---------- 历史页: 本地 ----------

// HistItem 本地历史条目(新命名 {jobID}.png 或旧 wallpaper_*.png)。
type HistItem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	At    string `json:"at"`    // "01-02 15:04"
	JobID string `json:"jobId"` // 新命名提取; 旧文件为空
	Fav   bool   `json:"fav"`   // 本地收藏清单(favs.json); jobId 行另与云端收藏合并(前端)
}

// isJobIDFile 判定文件名(去扩展)是否为 24 位十六进制任务ID(与安卓端 jobIdOf 同规则)。
func isJobIDFile(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// GetLocalHistory 本地壁纸留档(新→旧)。文件名规则:
//   - 新: {jobID}.png(jobs 表主键, 云端归档可反查, 召回去重靠它 os.Stat)
//   - 旧: wallpaper_{ts}*.png(历史兼容, 不参与去重)
func (b *Bridge) GetLocalHistory() []HistItem {
	b.mu.Lock()
	st := b.st
	dir := b.dir
	b.mu.Unlock()
	favs, _ := store.LoadLocalFavs(dir)
	entries, err := os.ReadDir(st.WallpapersDir())
	if err != nil {
		return []HistItem{}
	}
	type row struct {
		item HistItem
		mod  time.Time
	}
	var rows []row
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		jobID := ""
		if !strings.HasPrefix(name, "wallpaper_") {
			if id := strings.TrimSuffix(name, filepath.Ext(name)); isJobIDFile(id) {
				jobID = id
			}
		}
		rows = append(rows, row{
			item: HistItem{
				Name:  name,
				Path:  filepath.Join(st.WallpapersDir(), name),
				At:    info.ModTime().Format("01-02 15:04"),
				JobID: jobID,
				Fav:   favs[name],
			},
			mod: info.ModTime(),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].mod.After(rows[j].mod) })
	out := make([]HistItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.item)
	}
	return out
}

// SetLocalFav 本地收藏标记(旧时间戳命名文件用; {jobID}.png 行以云端收藏为准, 跨端同步)。
func (b *Bridge) SetLocalFav(name string, fav bool) error {
	b.mu.Lock()
	dir := b.dir
	b.mu.Unlock()
	if name == "" {
		return fmt.Errorf("文件名为空")
	}
	if err := store.SetLocalFav(dir, name, fav); err != nil {
		return fmt.Errorf("收藏保存失败: %s", friendlyErrText(err))
	}
	return nil
}

// DeleteLocal 删除本地图(与安卓长按删除一致): jobId 行先同步清理云端副本(失败降级只删本地),
// 再删本地文件。当前壁纸拒删 —— PC 预览直接读本地文件, 删后预览空白(安卓可读系统壁纸无此限)。
func (b *Bridge) DeleteLocal(name string) (string, error) {
	b.mu.Lock()
	cc := b.cc
	st := b.st
	dir := b.dir
	b.mu.Unlock()
	// 文件名只在本目录内反查, 不拼接用户输入(防穿越)
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(strings.ToLower(name), ".png") {
		return "", fmt.Errorf("文件名无效")
	}
	path := filepath.Join(st.WallpapersDir(), name)
	if cur := st.CurrentPath(); cur != "" &&
		strings.EqualFold(filepath.Clean(cur), filepath.Clean(path)) {
		return "", fmt.Errorf("这张正在使用中(当前壁纸) — 请先「立即换一张」, 再删除它")
	}
	jobID := ""
	if !strings.HasPrefix(name, "wallpaper_") {
		if id := strings.TrimSuffix(name, filepath.Ext(name)); isJobIDFile(id) {
			jobID = id
		}
	}
	cloudErrText := ""
	if jobID != "" && cc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := cc.DeleteImage(ctx, jobID)
		cancel()
		if err != nil {
			// 安卓同款降级: 云端清理失败不拦本地删除
			cloudErrText = ", 云端副本清理失败"
			b.st.Log("删除: 云端副本清理失败 job=%s: %v", jobID, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("文件不存在")
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("删除失败: %s", friendlyErrText(err))
	}
	_ = store.SetLocalFav(dir, name, false) // 顺带清掉本地收藏标记, 不留死条目
	switch {
	case jobID == "":
		return "已删除本地 ✓ (旧时间戳文件无云端副本)", nil
	case cloudErrText != "":
		return "已删除本地 ✓" + cloudErrText, nil
	default:
		return "已删除 ✓ 云端副本同步清理", nil
	}
}

// PreviewLocal 本地图预览(560×315 JPEG base64; 自动识别 PNG/JPEG 内容)。
func (b *Bridge) PreviewLocal(path string) (string, error) {
	return readFileB64(path, 560, 315)
}

// ApplyLocal 重新应用本地壁纸。
func (b *Bridge) ApplyLocal(path string) (string, error) {
	b.mu.Lock()
	st := b.st
	b.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("文件不存在")
	}
	if err := desktop.SetWallpaper(path); err != nil {
		return "", fmt.Errorf("应用失败: %s", friendlyErrText(err))
	}
	st.SetCurrent(path)
	_ = signals.Append(b.dir, signals.Event{
		Type: signals.TypeReapply, Detail: filepath.Base(path),
	})
	return "已重新应用。该类画风会被记为正向证据, 影响后续出图。", nil
}

// ---------- 历史页: 云端 ----------

// CloudItem 云端历史条目(列表行=心形+时间+规格, 不带提示词——提示词在预览区展示)。
type CloudItem struct {
	JobID     string `json:"jobId"`
	CreatedAt string `json:"createdAt"` // 已格式化 "01-02 15:04"
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Kind      string `json:"kind"`
	Fav       bool   `json:"fav"`
	HasLocal  bool   `json:"hasLocal"` // 本地已有 {jobID}.png(召回去重)
}

// CloudHistoryResult 云端历史(收藏置顶)。
type CloudHistoryResult struct {
	OK      bool        `json:"ok"`
	Items   []CloudItem `json:"items"`
	Message string      `json:"message,omitempty"`
}

// GetCloudHistory 拉取云端历史; offset 翻页(全部召回时循环调用直到返回少于 limit)。
func (b *Bridge) GetCloudHistory(offset int) CloudHistoryResult {
	b.mu.Lock()
	cc := b.cc
	st := b.st
	b.mu.Unlock()
	if cc == nil {
		return CloudHistoryResult{Message: "当前为直连调试模式, 无云端历史。"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	items, err := cc.GetHistoryPage(ctx, offset, 100)
	if err != nil {
		return CloudHistoryResult{Message: "云端历史加载失败: " + friendlyErrText(err)}
	}
	// 本地已有检测(新命名 {jobID}.png 直查, 旧时间戳文件不认——放下历史包袱)
	local := map[string]bool{}
	if entries, err := os.ReadDir(st.WallpapersDir()); err == nil {
		for _, e := range entries {
			if n := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())); !strings.HasPrefix(n, "wallpaper_") {
				local[n] = true
			}
		}
	}
	out := make([]CloudItem, 0, len(items))
	for _, it := range items {
		ts := it.CreatedAt
		if len(ts) >= 16 {
			ts = strings.Replace(ts[5:16], "T", " ", 1)
		}
		out = append(out, CloudItem{
			JobID: it.JobID, CreatedAt: ts,
			Width: it.Width, Height: it.Height, Kind: it.Kind, Fav: it.Fav,
			HasLocal: local[it.JobID],
		})
	}
	return CloudHistoryResult{OK: true, Items: out}
}

// previewCacheDir 预览缩略图本地缓存目录(无上限: 磁盘由用户自管, 与壁纸留档同哲学)。
// jobID 对应的图永不变 → 缓存只增不删, 无失效问题; 新图不在缓存才去下载。
func (b *Bridge) previewCacheDir() string { return filepath.Join(b.dir, "cache", "preview") }

// PreviewCloud 云端图预览: 缓存命中读盘(零网络), 未命中下缩略图(57KB)落盘后返回。
// 过期选中由前端丢弃。
func (b *Bridge) PreviewCloud(jobID string) (string, error) {
	b.mu.Lock()
	cc := b.cc
	dir := b.previewCacheDir()
	b.mu.Unlock()
	if cc == nil {
		return "", fmt.Errorf("云端客户端未初始化")
	}
	cachePath := filepath.Join(dir, jobID+".jpg")
	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
		return scaleToB64(data, 560, 315)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := cc.HistoryImageThumb(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("云端图片下载失败: %s", friendlyErrText(err))
	}
	// 落盘缓存(尽力而为, 失败不影响本次预览)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(cachePath, data, 0o644)
	}
	return scaleToB64(data, 560, 315)
}

// RecallResult 召回结果。
type RecallResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// RecallCloud 召回单张云端图: 下载 → 留档 {jobID}.png → 设为壁纸。
func (b *Bridge) RecallCloud(jobID string) RecallResult {
	b.mu.Lock()
	cc, st := b.cc, b.st
	b.mu.Unlock()
	if cc == nil || jobID == "" {
		return RecallResult{Message: "当前为直连调试模式, 无云端历史"}
	}
	localPath := filepath.Join(st.WallpapersDir(), jobID+".png")
	if _, err := os.Stat(localPath); err == nil {
		// 已有: 直接应用(不重复下载)
		if err := desktop.SetWallpaper(localPath); err != nil {
			return RecallResult{Message: "应用失败: " + friendlyErrText(err)}
		}
		st.SetCurrent(localPath)
		_ = signals.Append(b.dir, signals.Event{Type: signals.TypeReapply, Detail: jobID + ".png"})
		return RecallResult{OK: true, Path: localPath, Message: "本地已有, 已直接应用 ✓"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	data, err := cc.HistoryImage(ctx, jobID)
	if err != nil {
		return RecallResult{Message: "云端图片下载失败: " + friendlyErrText(err)}
	}
	path, err := b.saveNamed(jobID+".png", data)
	if err != nil {
		return RecallResult{Message: "留档失败: " + friendlyErrText(err)}
	}
	if err := desktop.SetWallpaper(path); err != nil {
		return RecallResult{Message: "应用失败: " + friendlyErrText(err)}
	}
	st.SetCurrent(path)
	_ = signals.Append(b.dir, signals.Event{Type: signals.TypeReapply, Detail: jobID + ".png"})
	return RecallResult{OK: true, Path: path, Message: "已召回并应用 ✓ 已留档到本地历史"}
}

// RecallAllProgress 全部召回进度(事件推送)。
type RecallAllProgress struct {
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	Total   int `json:"total"`
}

// RecallAllResult 全部召回汇总。
type RecallAllResult struct {
	OK      bool   `json:"ok"`
	Done    int    `json:"done"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	Message string `json:"message"`
}

// RecallAll 把云端全部本地图召回本地(逐页 offset 翻页; 已有 {jobID}.png 跳过)。
// 不设壁纸, 只落盘; 进度经 recall-progress 事件推送。
func (b *Bridge) RecallAll() RecallAllResult {
	b.mu.Lock()
	cc, st := b.cc, b.st
	b.mu.Unlock()
	if cc == nil {
		return RecallAllResult{Message: "当前为直连调试模式, 无云端历史"}
	}
	local := map[string]bool{}
	if entries, err := os.ReadDir(st.WallpapersDir()); err == nil {
		for _, e := range entries {
			if n := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())); !strings.HasPrefix(n, "wallpaper_") {
				local[n] = true
			}
		}
	}
	var done, skipped, failed int
	for offset := 0; ; offset += 100 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		items, err := cc.GetHistoryPage(ctx, offset, 100)
		cancel()
		if err != nil {
			return RecallAllResult{Done: done, Skipped: skipped, Failed: failed + 1,
				Message: "云端历史加载失败: " + friendlyErrText(err)}
		}
		for _, it := range items {
			if local[it.JobID] {
				skipped++
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			data, err := cc.HistoryImage(ctx, it.JobID)
			cancel()
			if err != nil {
				failed++
				b.st.Log("全部召回: 下载失败 job=%s: %v", it.JobID, err)
				continue
			}
			if _, err := b.saveNamed(it.JobID+".png", data); err != nil {
				failed++
				continue
			}
			local[it.JobID] = true
			done++
		}
		b.emit("recall-progress", RecallAllProgress{Done: done, Skipped: skipped, Failed: failed, Total: done + skipped + failed})
		if len(items) < 100 {
			break
		}
	}
	msg := fmt.Sprintf("已召回 %d 张", done)
	if skipped > 0 {
		msg += fmt.Sprintf(", 跳过本地已有 %d 张", skipped)
	}
	if failed > 0 {
		msg += fmt.Sprintf(", 失败 %d 张", failed)
	}
	return RecallAllResult{OK: failed == 0, Done: done, Skipped: skipped, Failed: failed, Message: msg}
}

// ToggleFav 收藏/取消收藏(乐观更新由前端做, 这里落服务端)。
func (b *Bridge) ToggleFav(jobID string, fav bool) (bool, error) {
	b.mu.Lock()
	cc := b.cc
	b.mu.Unlock()
	if cc == nil {
		return false, fmt.Errorf("当前为直连调试模式, 无云端历史")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cc.HistoryFav(ctx, jobID, fav); err != nil {
		return !fav, fmt.Errorf("收藏失败: %s", friendlyErrText(err))
	}
	return fav, nil
}

// GetFavs 收藏任务ID集合: 本地历史行星标显示与"只看收藏"筛选
// (收藏态存服务端 archive 表, 文件名 {jobID}.png 反查关联; 旧时间戳文件无法关联)。
func (b *Bridge) GetFavs() ([]string, error) {
	b.mu.Lock()
	cc := b.cc
	b.mu.Unlock()
	if cc == nil {
		return nil, fmt.Errorf("当前为直连调试模式, 无云端收藏")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ids, err := cc.GetFavJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("收藏列表获取失败: %s", friendlyErrText(err))
	}
	return ids, nil
}
