package skill

import "encoding/json"

// ——— 技能域的读/写视图（v3.2 刀 4）———
//
// 领域**不产出对外 JSON 形态**：这些结构只有字段、没有 tag，也不认识 omitempty。
// 对外形态（`dtocapability.Skill*`）由用例层 `service/svccapability` 投影——它才是
// "HTTP 契约长什么样"的负责人。这样改一个 JSON 字段名不必动领域代码，领域测试也不必断言线上格式。
//
// 字段名与对外 DTO 一一对应是**刻意的**：投影是纯搬运（逐字段赋值），不做重命名，
// 于是"哪些字段真的传出去了"在用例层一眼可见，不会在两套命名之间产生第二份语义。

// SkillView 技能读面的一条。
type SkillView struct {
	ID          string
	Name        string
	Description string
	Body        string
	Runtimes    []string
	Scope       string
	Enabled     bool
	Warnings    []WarningView
}

// WarningView 一条正文扫描告警（severity = info|warning|critical；出现在读面里的不会是 critical）。
type WarningView struct {
	Severity string
	Pattern  string
	Message  string
}

// WriteRequest 定义写面的**原文对象**：键的语义（`null` = 删键）由 `internal/core/definition.RawPatch` 解释。
//
// 用 `json.RawMessage` 而不是解成结构体：嵌套对象/数组原样落盘，不丢键、不变形。
type WriteRequest map[string]json.RawMessage

// WriteResult 新建/覆盖定义的结果。
type WriteResult struct {
	ID                string
	Path              string
	Created           bool
	Written           int
	Removed           int
	Warnings          []WarningView
	MaterializeErrors []string
	NextAction        string
}

// DeleteResult 删除定义的结果（不存在 → Deleted=false 且无错：DELETE 是幂等语义）。
type DeleteResult struct {
	ID                string
	Deleted           bool
	Path              string
	Removed           int
	MaterializeErrors []string
	NextAction        string
}

// ToggleResult 启停定义的结果。
type ToggleResult struct {
	ID                string
	Enabled           bool
	Path              string
	Written           int
	Removed           int
	MaterializeErrors []string
	NextAction        string
}

// MaterializationView 一个 skill 在每处物化目标上的体检结果。
type MaterializationView struct {
	ID      string
	Enabled bool
	States  []MaterializationStateView
	Notes   []string
}

// MaterializationStateView 一处物化目标的现状。
//
// Status 取值（每个都可判定）：in_sync / stale / tampered / missing / disabled /
// not_configured / not_applicable / user_owned。
type MaterializationStateView struct {
	Root       string
	Runtime    string
	Dir        string
	Status     string
	Hash       string
	ActualHash string
	WrittenAt  string
	Owners     []string
	Detail     string
}
