// Package pool 本地备用壁纸池: 静默/空闲时段预取到本地的"已生成未使用"壁纸。
// 换图时优先消耗池中成品(秒换, 断网可用); 画像版本变化后旧池自动作废;
// 池目标张数由策略(policy.PoolTarget)控制, 本包另设硬上限防御异常膨胀。
package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MaxKeep 池条目硬上限(防御用; 正常申请节奏由策略控制)。
const MaxKeep = 8

// Entry 池中一张壁纸的元数据(对应云端预生成成品)。
type Entry struct {
	File           string            `json:"file"`
	ProfileVersion int               `json:"profile_version"`
	Positive       string            `json:"positive"`
	Negative       string            `json:"negative"`
	Combo          map[string]string `json:"combo"`
	Seed           int64             `json:"seed"`
	WorkflowID     string            `json:"workflow_id"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	CreatedAt      string            `json:"created_at"` // RFC3339
}

// Pending 在途补池单: 已向云端提交预生成, 等待出图完成后提货入池。
type Pending struct {
	JobID          string `json:"job_id"`
	ProfileVersion int    `json:"profile_version"`
	Since          string `json:"since"` // RFC3339
}

type metaFile struct {
	Entries []Entry `json:"entries"`
}

func poolDir(root string) string  { return filepath.Join(root, "pool") }
func metaPath(root string) string { return filepath.Join(poolDir(root), "meta.json") }
func pendPath(root string) string { return filepath.Join(poolDir(root), "pending.json") }

// Count 池中张数。
func Count(root string) int { return len(loadEntries(root)) }

// List 返回池中条目(按入池时间升序)。
func List(root string) []Entry {
	es := loadEntries(root)
	sort.SliceStable(es, func(i, j int) bool { return es[i].CreatedAt < es[j].CreatedAt })
	return es
}

// Add 入池: 写图片文件并更新元数据; 超出硬上限时丢弃最旧条目。
func Add(root string, e Entry, img []byte) error {
	if err := os.MkdirAll(poolDir(root), 0o755); err != nil {
		return err
	}
	e.File = fmt.Sprintf("p-%d.jpg", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(poolDir(root), e.File), img, 0o644); err != nil {
		return err
	}
	es := loadEntries(root)
	es = append(es, e)
	sort.SliceStable(es, func(i, j int) bool { return es[i].CreatedAt < es[j].CreatedAt })
	for len(es) > MaxKeep {
		_ = os.Remove(filepath.Join(poolDir(root), es[0].File))
		es = es[1:]
	}
	return saveEntries(root, es)
}

// Take 取最早入池的一张(读取图片并从池移除); 池空返回 nil, nil, nil。
func Take(root string) (*Entry, []byte, error) {
	es := List(root)
	if len(es) == 0 {
		return nil, nil, nil
	}
	e := es[0]
	img, err := os.ReadFile(filepath.Join(poolDir(root), e.File))
	if err != nil {
		// 文件损坏/丢失: 清理该条目(下轮自然补池)
		_ = saveEntries(root, es[1:])
		return nil, nil, nil
	}
	_ = os.Remove(filepath.Join(poolDir(root), e.File))
	if err := saveEntries(root, es[1:]); err != nil {
		return nil, nil, err
	}
	return &e, img, nil
}

// CleanVersion 清理非当前画像版本的条目, 返回清理张数。
func CleanVersion(root string, version int) int {
	es := loadEntries(root)
	kept := make([]Entry, 0, len(es))
	removed := 0
	for _, e := range es {
		if e.ProfileVersion != version {
			_ = os.Remove(filepath.Join(poolDir(root), e.File))
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed > 0 {
		_ = saveEntries(root, kept)
	}
	return removed
}

// ---------- 在途补池单 ----------

// LoadPending 读取在途补池单; 无则返回 nil。
func LoadPending(root string) *Pending {
	raw, err := os.ReadFile(pendPath(root))
	if err != nil {
		return nil
	}
	var p Pending
	if json.Unmarshal(raw, &p) != nil || p.JobID == "" {
		return nil
	}
	return &p
}

// SavePending 记录在途补池单。
func SavePending(root string, p Pending) error {
	if err := os.MkdirAll(poolDir(root), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pendPath(root), raw, 0o644)
}

// ClearPending 清除在途补池单。
func ClearPending(root string) { _ = os.Remove(pendPath(root)) }

func loadEntries(root string) []Entry {
	raw, err := os.ReadFile(metaPath(root))
	if err != nil {
		return nil
	}
	var m metaFile
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m.Entries
}

func saveEntries(root string, es []Entry) error {
	if err := os.MkdirAll(poolDir(root), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(metaFile{Entries: es}, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(root) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(root))
}
