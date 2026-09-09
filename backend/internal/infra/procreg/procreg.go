// Package procreg 维护 daemon 拉起的 runtime 子进程注册表（JSON），用于 daemon 被
// SIGKILL/崩溃后，下一次启动兜底回收上一实例遗留的孤儿进程（决策 v4③：
// 「daemon 侧进程注册表 JSON 兜底回收孤儿进程」）。
//
// 设计要点（宁可漏杀，不可误杀）：
//   - 每个条目记录 pid / pgid / 命令 / runtime / agent / 启动时刻 / daemon 实例 id；
//   - 只有「非本实例」登记的条目才是候选孤儿；
//   - 进程不存在 → 只清账，不报错；
//   - 进程存在但当前 pgid 与登记值不一致 → 判定 PID 被复用（或已脱离进程组），跳过并留日志；
//   - 一致 → 先 SIGTERM 整个进程组，宽限期后仍存活则 SIGKILL。
package procreg

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry 一条子进程登记。
type Entry struct {
	PID       int       `json:"pid"`
	PGID      int       `json:"pgid"`
	Command   string    `json:"command"`
	Runtime   string    `json:"runtime"`
	AgentID   string    `json:"agent_id"`
	StartedAt time.Time `json:"started_at"`
	Instance  string    `json:"instance"`
}

// Report 一次启动回收的结果。
type Report struct {
	Reaped   []Entry // 已回收（SIGTERM/SIGKILL）
	Cleaned  []Entry // 进程已不存在，仅清账
	Skipped  []Entry // PID 复用/不可判定，跳过
	Disabled bool    // 注册表未启用
}

// Total 返回处理过的条目数。
func (r Report) Total() int { return len(r.Reaped) + len(r.Cleaned) + len(r.Skipped) }

// Registry 进程注册表（进程内状态 + 单文件持久化）。
type Registry struct {
	path     string
	mu       sync.Mutex
	instance string
	entries  map[int]Entry
}

// Open 打开（或新建）注册表：path 为空返回 nil（禁用，所有方法对 nil 接收者安全）。
// 文件不存在或损坏不阻塞启动：损坏时以空表继续并保留 .corrupt 备份。
func Open(path string) (*Registry, error) {
	if path == "" {
		return nil, nil
	}
	r := &Registry{
		path:     path,
		instance: newInstanceID(),
		entries:  map[int]Entry{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("procreg: 读取 %s: %w", path, err)
	}
	if len(data) == 0 {
		return r, nil
	}
	var file registryFile
	if err := json.Unmarshal(data, &file); err != nil {
		bak := path + ".corrupt"
		_ = os.WriteFile(bak, data, 0o600)
		slog.Warn("procreg: 注册表损坏，已备份并重建", "path", path, "backup", bak, "err", err)
		return r, nil
	}
	for _, e := range file.Entries {
		if e.PID > 0 {
			r.entries[e.PID] = e
		}
	}
	return r, nil
}

type registryFile struct {
	Version   int     `json:"version"`
	UpdatedAt string  `json:"updated_at"`
	Entries   []Entry `json:"entries"`
}

const registryVersion = 1

// Instance 本实例 id（写入新条目用；用于区分「上一实例的遗留」）。
func (r *Registry) Instance() string {
	if r == nil {
		return ""
	}
	return r.instance
}

// Path 注册表文件路径（空 = 禁用）。
func (r *Registry) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Add 登记一条子进程（同 pid 覆盖）。
func (r *Registry) Add(e Entry) error {
	if r == nil {
		return nil
	}
	if e.PID <= 0 {
		return fmt.Errorf("procreg: pid 必须为正数")
	}
	if e.Instance == "" {
		e.Instance = r.instance
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[e.PID] = e
	return r.persistLocked()
}

// Remove 摘除一条登记（进程已由正常路径回收时调用）。
func (r *Registry) Remove(pid int) error {
	if r == nil || pid <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[pid]; !ok {
		return nil
	}
	delete(r.entries, pid)
	return r.persistLocked()
}

// List 返回全部登记（按 pid 升序，便于日志稳定）。
func (r *Registry) List() []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Orphans 返回「非本实例」登记的条目（候选孤儿）。
func (r *Registry) Orphans() []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Entry
	for _, e := range r.entries {
		if e.Instance != r.instance {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Reap 回收上一实例遗留的孤儿进程（grace 为 SIGTERM → SIGKILL 的宽限期）。
func (r *Registry) Reap(grace time.Duration) (Report, error) {
	var rep Report
	if r == nil {
		rep.Disabled = true
		return rep, nil
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	orphans := r.Orphans()
	for _, e := range orphans {
		if !alive(e.PID) {
			rep.Cleaned = append(rep.Cleaned, e)
			_ = r.Remove(e.PID)
			continue
		}
		// PID 复用保护：登记了 pgid 时，当前 pgid 必须一致才认为是我们拉起的进程组。
		if e.PGID > 0 {
			if cur := pgidOf(e.PID); cur != e.PGID {
				rep.Skipped = append(rep.Skipped, e)
				slog.Warn("procreg: 跳过疑似 PID 复用的登记（pgid 不一致）",
					"pid", e.PID, "want_pgid", e.PGID, "got_pgid", cur, "command", e.Command)
				_ = r.Remove(e.PID)
				continue
			}
		}
		if err := killGroup(e, termSignal()); err != nil && !gone(err) {
			rep.Skipped = append(rep.Skipped, e)
			slog.Warn("procreg: 终止孤儿进程失败", "pid", e.PID, "pgid", e.PGID, "err", err)
			continue
		}
		if !waitGone(e.PID, grace) {
			if err := killGroup(e, killSignal()); err != nil && !gone(err) {
				slog.Warn("procreg: 强杀孤儿进程失败", "pid", e.PID, "err", err)
			}
			waitGone(e.PID, grace)
		}
		rep.Reaped = append(rep.Reaped, e)
		_ = r.Remove(e.PID)
	}
	if rep.Total() > 0 {
		slog.Info("procreg: 启动孤儿回收完成",
			"reaped", len(rep.Reaped), "cleaned", len(rep.Cleaned), "skipped", len(rep.Skipped))
	}
	return rep, nil
}

// ClearAll 清空全部登记（仅用于测试/显式清理，不杀进程）。
func (r *Registry) ClearAll() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = map[int]Entry{}
	return r.persistLocked()
}

func (r *Registry) persistLocked() error {
	entries := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PID < entries[j].PID })
	file := registryFile{Version: registryVersion, UpdatedAt: time.Now().Format(time.RFC3339), Entries: entries}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("procreg: 序列化注册表: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("procreg: 创建目录: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("procreg: 写入 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("procreg: 原子替换 %s: %w", r.path, err)
	}
	return nil
}

func newInstanceID() string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}
