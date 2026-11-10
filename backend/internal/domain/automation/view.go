package automation

import "github.com/morehao/ark-mate/internal/core/automation"

// ——— 自动化域的对外响应形状（v3.2 刀 7）———
//
// 只有字段、没有 json tag：对外形态（`dtoautomation.AutomationResp`）由用例层
// `service/svcautomation` 投影。字段名与 DTO 一一对应（投影是纯搬运）。
//
// `Schedule` / `Target` / `RunRecord` 直接用 `internal/core/automation` 的值类型：那是本仓的
// **调度词汇**（L1），不是传输结构——它们本来就没有 json tag，由 DTO 侧另建同名映射。
type AutomationView struct {
	ID             string
	Name           string
	Enabled        bool
	State          string
	Schedule       automation.Schedule
	Target         automation.Target
	MaxRunDuration string
	NextDueAt      int64

	LastFiredAt int64

	LastSessionID       string
	LastStatus          string
	LastReason          string
	LastReasonKind      string
	ConsecutiveFailures int
	Error               string
	RecentRuns          []automation.RunRecord
}
