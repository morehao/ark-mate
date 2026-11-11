package automation

import "time"

// Automation 的状态机（对齐 openwork `state.ts:3-13`，去掉本平台不需要的维度）：
//
//	active ⇄ inactive ⇄ needs_attention → archived
//
// `archived` 是**终态**（归档即退出调度，不再改写）。`needs_attention` 不是终态：
// 它是"这轮没跑成，等用户修"的显式停留点——停留在那里是刻意的，因为自动重试在单操作员
// 场景下是纯风险放大（prompt 往往有副作用：改代码、发通知）。
const (
	StateActive         = "active"
	StateInactive       = "inactive"
	StateNeedsAttention = "needs_attention"
	StateArchived       = "archived"
)

// NormalizeState 空值 = `active`（手工写的定义文件通常不写 state，语义上就是启用待跑）。
// 未知取值也按 `active` 处理：定义是用户可编辑的 JSON，一个拼错的状态名不该让整条 automation
// 从调度里消失（与"坏定义告警跳过"的既有纪律不同——那里是定义本身不可执行，这里只是状态字段脏）。
func NormalizeState(s string) string {
	switch s {
	case StateActive, StateInactive, StateNeedsAttention, StateArchived:
		return s
	default:
		return StateActive
	}
}

// Schedulable 只有 `active` 参与调度；`enabled=false` 由调用方另行判断（两者是不同维度：
// `enabled` 是用户的开关，`state` 是运行态）。
func Schedulable(state string) bool { return NormalizeState(state) == StateActive }

// Terminal 终态：归档后不再有任何自动流转。
func Terminal(state string) bool { return NormalizeState(state) == StateArchived }

// CanTransition 状态流转是否合法。同态视为合法（幂等写回）。
func CanTransition(from, to string) bool {
	f, t := NormalizeState(from), NormalizeState(to)
	if f == StateArchived {
		return t == StateArchived // archived 是终态：出不去
	}
	if f == t {
		return true
	}
	switch t {
	case StateActive, StateInactive, StateNeedsAttention, StateArchived:
		return true
	}
	return false
}

// RunStatus 一次触发的认领结果（与 `RunStatus`/会话状态同词表，前端不必再学一套）。
//
// `claimed` 与其余四个**不是同一层**：`claimed` = "会话已建、这轮跑起来了"，结果由**会话自身**
// 的终态承载（ADR-4：不建第二套 run 状态机）；后四个是"这一轮没能跑成"的终态。
// 平台不在会话结束时回写 overlay：会话状态才是最权威的运行态，回写只会造出第二个会漂移的事实源。
const (
	RunClaimed     = "claimed"   // 已认领并建出会话（结果看会话）
	RunDuplicate   = "duplicate" // 该 occurrence 已经认领过（进程重启/写回失败后的唯一防线）
	RunOverlap     = "overlap"   // 上一次会话还在跑 → 跳过本次（不排队）
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
	RunSkipped     = "skipped"
)

// RunRecord 一次触发的留痕（定义文件的 `recent_runs` 环形缓冲，最新在前）。
//
// 为什么留痕落**定义文件**而不是 events 表：events 表的 `session_id` 是
// `NOT NULL REFERENCES sessions(id)`（DDL 与 DSN 的 foreign_keys(1) 都在，
// `internal/infra/dbclient` 有守卫测试），而 `duplicate`/`overlap`/`skipped` 这三类结果**根本没有会话可挂**——
// 硬塞一个假 agg id 会直接撞外键。与其放宽约束，不如把"最近 N 次"放在文件里：
// 它本来就已经在每次触发时被重写一次（overlay），多带一个环形缓冲不增加写次数，
// 且 memory 模式（无 events 表）下运行史照样在。
type RunRecord struct {
	// ScheduledFor 本次认领的 occurrence（UTC）。
	ScheduledFor time.Time `json:"scheduled_for"`
	FiredAt      time.Time `json:"fired_at"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
	// ReasonKind 失败原因码（机器读；见 ReasonKind* 常量）。空 = 无稳定原因码（如瞬时失败/未分类）。
	ReasonKind string `json:"reason_kind,omitempty"`
	// SessionID 本次产生的会话（`claimed` 之外为空）。
	SessionID string `json:"session_id,omitempty"`
	// Trigger scheduled | recovery | manual。
	Trigger string `json:"trigger,omitempty"`
}

// RecentRunsMax 定义文件里保留的最近运行条数。
//
// 20 是"够用且不臃肿"的取舍：面板的运行史是给人看的（一次排查看最近几次就够），
// 而每条约 150 字节，20 条 ≈ 3KB——仍是一个运维愿意直接打开读的文件。
const RecentRunsMax = 20
