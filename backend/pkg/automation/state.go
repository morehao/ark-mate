package automation

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

// RunStatus 一次触发的终态（与 `TurnStatus`/会话状态同词表，前端不必再学一套）。
const (
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
	RunSkipped     = "skipped"
)
