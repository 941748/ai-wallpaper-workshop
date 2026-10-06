// Package store 负责数据目录管理: 文件锁防重入 / 日志轮转 / 壁纸留档与清理 / 历史记录。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"wallpaper/internal/config"
	"wallpaper/internal/prompt"
)

// MaxHistory 提示词历史保留条数。
const MaxHistory = 200

// KeepWallpapers 留档壁纸数量。
const KeepWallpapers = 30

// LogMaxSize 单个日志文件上限(1MB)。
const LogMaxSize = 1 << 20

// LogBackups 日志备份份数。
const LogBackups = 3

// Store 数据目录句柄。
type Store struct {
	Dir string
}

// Open 打开(并创建)数据目录。
func Open(dir string) (*Store, error) {
	if err := config.EnsureSubdirs(dir); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

// ---------- 文件锁 ----------

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// ErrBusy 已有实例在运行。
var ErrBusy = fmt.Errorf("已有实例在运行")

// Lock 加文件锁防重入; 进程退出(含崩溃)时锁自动释放。
// 返回的 release 在流程结束时调用(可 defer)。
func (s *Store) Lock() (func(), error) {
	path := filepath.Join(s.Dir, "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	r1, _, errno := procLockFileEx.Call(
		f.Fd(),
		lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 == 0 {
		_ = f.Close()
		if e, ok := errno.(syscall.Errno); ok && e == windows.ERROR_LOCK_VIOLATION {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("LockFileEx: %w", errno)
	}
	return func() {
		procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		_ = f.Close()
	}, nil
}

// ---------- 日志 ----------

// Log 追加一行日志(带时间戳), 超过 1MB 轮转(共 3 份备份)。
func (s *Store) Log(format string, args ...any) {
	path := filepath.Join(s.Dir, "logs", "runtime.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > LogMaxSize {
		rotate(path, LogBackups)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(f, "%s %s\n", ts, fmt.Sprintf(format, args...))
}

func rotate(path string, backups int) {
	for i := backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// ---------- 壁纸留档 ----------

// WallpapersDir 壁纸目录。
func (s *Store) WallpapersDir() string { return filepath.Join(s.Dir, "wallpapers") }

// ProbesDir 探针图目录。
func (s *Store) ProbesDir() string { return filepath.Join(s.Dir, "probes") }

// BinDir 程序副本目录(计划任务指向)。
func (s *Store) BinDir() string { return filepath.Join(s.Dir, "bin") }

// SaveWallpaper 保存成品图并返回路径(同一时间戳追加序号避免覆盖)。
func (s *Store) SaveWallpaper(data []byte, at time.Time) (string, error) {
	base := "wallpaper_" + at.Format("20060102_150405")
	path := filepath.Join(s.WallpapersDir(), base+".png")
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(s.WallpapersDir(), fmt.Sprintf("%s_%d.png", base, i))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// SetCurrent 记录当前正在使用的壁纸路径(清理时排除, 防误删变纯色背景)。
func (s *Store) SetCurrent(path string) {
	_ = os.WriteFile(filepath.Join(s.WallpapersDir(), "current.txt"), []byte(path), 0o644)
}

// CurrentPath 返回当前壁纸路径(可能为空)。
func (s *Store) CurrentPath() string {
	b, err := os.ReadFile(filepath.Join(s.WallpapersDir(), "current.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// CleanupWallpapers 仅保留最近 keep 张(排除 current 与 .old), 其余删除。
func (s *Store) CleanupWallpapers(keep int) error {
	dir := s.WallpapersDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type item struct {
		path string
		mod  time.Time
	}
	var imgs []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "wallpaper_") || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		imgs = append(imgs, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(imgs) <= keep {
		return nil
	}
	sort.Slice(imgs, func(a, b int) bool { return imgs[a].mod.After(imgs[b].mod) })
	current := s.CurrentPath()
	for _, it := range imgs[keep:] {
		if current != "" && strings.EqualFold(it.path, current) {
			continue // 当前使用中文件永不删除
		}
		_ = os.Remove(it.path)
	}
	return nil
}

// ---------- 历史记录 ----------

// LoadHistory 读取提示词历史。
func (s *Store) LoadHistory() ([]prompt.HistoryEntry, error) {
	var h []prompt.HistoryEntry
	raw, err := os.ReadFile(filepath.Join(s.Dir, "prompt_history.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return h, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return h, nil
}

// AppendHistory 追加一条历史并裁剪到 MaxHistory。
func (s *Store) AppendHistory(e prompt.HistoryEntry) error {
	h, err := s.LoadHistory()
	if err != nil {
		h = nil
	}
	h = append(h, e)
	if len(h) > MaxHistory {
		h = h[len(h)-MaxHistory:]
	}
	return writeJSON(filepath.Join(s.Dir, "prompt_history.json"), h)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
