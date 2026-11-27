package session

import (
	"sort"
	"time"
)

// TierSnapshot 是一条会话在某一时刻的**分层只读快照**（M3-3）：它回答"这条会话的日志现在该在哪一层"，
// 供诊断/运维只读查看。**不做任何搬迁或驱逐**——真正的 relocate/delete 是部署期控制面的动作。
type TierSnapshot struct {
	SessionID    string
	Tier         Tier
	LastActiveAt int64 // Unix 毫秒（0 = 未记录）
}

// TieringSnapshot 用给定策略对**当前进程登记在册的会话**判层，返回按 sessionID 升序的只读快照。
//
// 只读、无 IO：持本域锁遍历内存记录、逐条调纯判决 `TieringPolicy.Tier`，锁外排序返回——不读库、
// 不改任何状态，也不碰执行路径（与"真搬日志/真驱逐"彻底分开，避免把只读诊断变成有副作用的操作）。
//
// tombstone 恒取零值：已删会话不在内存会话表里（删除走移除），故快照里不会出现"已删但还占层"的情况；
// 这正是 TieringPolicy 仍保留 tombstone 参数的原因（面向未来控制面按库全量判层时使用）。
func (pl *Plane) TieringSnapshot(now time.Time, policy TieringPolicy) []TierSnapshot {
	pl.mu.Lock()
	out := make([]TierSnapshot, 0, len(pl.sessions))
	for _, r := range pl.sessions {
		last := time.Time{}
		if r.LastActiveAt > 0 {
			last = time.UnixMilli(r.LastActiveAt)
		}
		out = append(out, TierSnapshot{
			SessionID:    r.ID,
			Tier:         policy.Tier(last, time.Time{}, now),
			LastActiveAt: r.LastActiveAt,
		})
	}
	pl.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}
