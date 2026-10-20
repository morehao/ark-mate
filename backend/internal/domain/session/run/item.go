// Package run 是会话域的**轮次值类型**（叶子包，理由见 `session/queue` 的包注释）：
// 域与 store 适配器共同依赖它，但谁都不 import 对方，避免 Go 的 test import cycle。
package run

import "time"

// Item 一轮 prompt 的实体（一张 `runs` 行的完整形状）。
//
// 生命周期：开轮即落行（`status=in_progress`、`first_seq`=起点位次、`last_seq=-1`、`ended_at`
// 空），终态只 UPDATE。崩溃/停机时进行中的轮留在 `in_progress`——那是事实，不是缺陷。
type Item struct {
	// ID 平台铸号 `run_<26 位 ULID>`。
	ID string
	// SessionID 所属会话。
	SessionID string
	// UserText 本轮的用户输入（开轮时写入）。
	UserText string
	// Status 轮次状态（`in_progress` 或终态；终态取值权威定义在 `domain/session/events.go`）。
	Status     string
	ErrorCode  string
	StopReason string
	UsageJSON  string
	// FirstSeq 本轮窗口起点（`run.started` 的会话内位次）。
	FirstSeq int64
	// LastSeq 本轮窗口终点；-1 = 还没收口。
	LastSeq int64
	// FirstTokenMS 从起点到首个 assistant 输出的耗时（ms）；0 = 未记录。
	FirstTokenMS int64
	// StartedAt 开轮时刻。
	StartedAt time.Time
	// EndedAt 终态时刻；未结束 → 零值。
	EndedAt time.Time
}

// Finish 一次 run 收口要写回的整组列（终态 + 窗口右端 + 首令牌耗时）。
//
// 成群传而不是八个位置参数：这几列是同一件事（"这一轮到此为止"）的各个面，分成位置参数时
// "少传一个"编译期无感，而写错的后果是静默的（窗口停在 -1 / 首令牌耗时恒为 0）。
type Finish struct {
	// LastSeq 窗口终点（终态事件的会话内位次）。
	LastSeq int64
	// Status 终态（completed / failed / cancelled）。
	Status     string
	StopReason string
	ErrorCode  string
	UsageJSON  string
	// FirstTokenMS 起点到首个 assistant 输出的耗时（ms）；0 = 无输出/未记录。
	FirstTokenMS int64
	// EndedAt 终态时刻（零值 → 落库为空串）。
	EndedAt time.Time
}
