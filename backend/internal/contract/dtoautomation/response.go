package dtoautomation

import "github.com/morehao/ark-mate/internal/platform/automation"

// AutomationResp 一条 automation 的对外形态（定义 + 运行态 + 内存态 next_due）。
//
// `next_due_at` 是**内存态**（由认领游标与 schedule 现算，不落盘——落盘只会制造第二个事实源），
// 所以它在定义刚被外部改动、还没重新装载时可能短暂缺失；`last_*` 与 `recent_runs` 则来自文件。
type AutomationResp struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Enabled  bool                `json:"enabled"`
	State    string              `json:"state"`
	Schedule automation.Schedule `json:"schedule"`
	Target   automation.Target   `json:"target"`
	// MaxRunDuration 本轮外层超时（空 = 只用 MATE_RUN_TIMEOUT 看门狗）。
	MaxRunDuration string `json:"max_run_duration,omitempty"`
	// NextDueAt 下次到期（RFC3339 UTC；空 = 没有下一次：跑完的 once / 归档 / 装载失败）。
	NextDueAt string `json:"next_due_at,omitempty"`
	// LastFiredAt 最近一次**认领**的 occurrence（不论结果；ADR-10 的推进点）。
	LastFiredAt   string `json:"last_fired_at,omitempty"`
	LastSessionID string `json:"last_session_id,omitempty"`
	LastStatus    string `json:"last_status,omitempty"`
	LastReason    string `json:"last_reason,omitempty"`
	// ConsecutiveFailures 连续没跑成的次数（跑成一次即清零）。>0 时面板要显眼提示。
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`
	// Error 定义装载失败的原因（**坏定义也会出现在列表里**：写得不对却"列表里没有、
	// 调度也不跑、日志里一行 WARN"是最难排查的一类投诉）。
	Error string `json:"error,omitempty"`
	// RecentRuns 最近若干次触发（最新在前，最多 20 条）。
	RecentRuns []automation.RunRecord `json:"recent_runs,omitempty"`
}

// AutomationDeleteResp 删除结果（幂等：本来就不存在 → deleted=false 且无错）。
type AutomationDeleteResp struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// AutomationPreviewResp 预览结果：**每一条都带本地墙钟与 UTC 两个表示**。
//
// 只给 UTC 等于让用户自己换算——而"我设的 9 点到底几点跑"正是这个端点要回答的问题。
type AutomationPreviewResp struct {
	// Occurrences 到期时刻（按时间升序）。
	Occurrences []AutomationPreviewItem `json:"occurrences"`
	// Warnings 全局告警（如 DST 空档导致某个本地时刻不存在而被前移）。
	Warnings []string `json:"warnings,omitempty"`
}

// AutomationPreviewItem 一个预览时刻。
type AutomationPreviewItem struct {
	// At 该次到期的绝对时刻（RFC3339 UTC）。
	At string `json:"at"`
	// Wall 本地墙钟（`2006-01-02 15:04`），即"用户设定意图"的那一面。
	Wall string `json:"wall"`
	// Timezone 解释用的 IANA 时区。
	Timezone string `json:"timezone"`
	// Warnings 该次特有的告警（DST 空档/回拨等）。
	Warnings []string `json:"warnings,omitempty"`
}

// AutomationRunsResp 运行史（最新在前）。
type AutomationRunsResp struct {
	ID string `json:"id"`
	// Runs 最近若干次触发的留痕（含 claimed/overlap/duplicate/skipped：**没跑成的那些也要看得见**）。
	Runs []automation.RunRecord `json:"runs"`
	// Limit 本次返回的条数上限（服务端固定 RecentRunsMax；回带是为了让面板不必猜）。
	Limit int `json:"limit"`
}

// AutomationRunResp 手动触发一次的结果（`POST /automations/:id/run`）。
type AutomationRunResp struct {
	ID string `json:"id"`
	// Status claimed | overlap | duplicate | skipped（见 internal/platform/automation 的 RunStatus）。
	Status string `json:"status"`
	// Trigger 固定为 manual（同一次触发的其余可能值：scheduled/recovery）。
	Trigger string `json:"trigger"`
	// SessionID 本次产生的会话（status=claimed 时非空）。
	SessionID string `json:"session_id,omitempty"`
	// Reason 结果说明（人话；overlap/skipped 时说明为什么没跑）。
	Reason string `json:"reason,omitempty"`
	// State 触发后的状态（needs_attention = 没跑成，需要人看一眼）。
	State string `json:"state,omitempty"`
}
