package session

import (
	"sort"
	"time"
)

// ResidencyDecision 是一条驻留会话在某一时刻的**驻留调度只读快照**（M3-1）：它回答"这条驻留会话
// 现在是否会被温→冷驱逐"。**不做任何驱逐**——真驱逐（flush 后释放 Session 句柄）是调度控制面的动作。
type ResidencyDecision struct {
	SessionID    string
	Executing    bool      // 正在跑 turn（绝不驱逐）
	LastActiveAt time.Time // 最近活跃（LRU 依据；零值=未记录）
	Evict        bool      // 依当前策略是否应温→冷驱逐
}

// ResidencySnapshot 用给定策略对**当前挂着的活跃句柄**判驱逐，返回按 sessionID 升序的只读快照。
//
// 只读、无 IO：取句柄只读切片 → 逐条读 `Busy()`（executing）与 `Plane.Rec(...).LastActiveAt`
// → 交给纯判决 `ResidencyPolicy.Evictions` → 标记每条是否在驱逐集里。**不做真正的 Evict/Close**——
// 与 `TieringSnapshot` 同一条纪律：把只读诊断与有副作用的执行彻底分开。
//
// 口径：驻留集 = 活跃句柄表（`Handles`）。已断连/已摘除的会话不在其中；executing 由 `Busy()` 判。
// 这是"当前进程视角"的驻留快照——多 worker 时每个 worker 各自一份（与 C3「每用户每 worker」口径一致）。
func (r *Runner) ResidencySnapshot(now time.Time, policy ResidencyPolicy) []ResidencyDecision {
	handles := r.handles.Snapshot()
	inputs := make([]ResidentSession, 0, len(handles))
	lastByID := make(map[string]time.Time, len(handles))
	for _, as := range handles {
		var last time.Time
		if rec := r.plane.Rec(as.ID); rec != nil && rec.LastActiveAt > 0 {
			last = time.UnixMilli(rec.LastActiveAt)
		}
		lastByID[as.ID] = last
		inputs = append(inputs, ResidentSession{ID: as.ID, Executing: as.Busy(), LastActive: last})
	}
	evict := make(map[string]bool)
	for _, id := range policy.Evictions(inputs) {
		evict[id] = true
	}
	out := make([]ResidencyDecision, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, ResidencyDecision{
			SessionID:    in.ID,
			Executing:    in.Executing,
			LastActiveAt: lastByID[in.ID],
			Evict:        evict[in.ID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}
