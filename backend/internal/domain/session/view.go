package session

// ——— 会话读面的对外形状（v3.2 刀 9）———
//
// 只有字段、没有 json tag：对外形态（`dtosession.*`）由用例层投影。
// 字段名与 DTO 一一对应（投影是纯搬运）。

// SessionItem 一条会话概要。
type SessionItem struct {
	ID                 string
	RuntimeSessionID   string
	RuntimeID          string
	Status             string
	Run                *RunPhase
	Title              string
	Pinned             bool
	Archived           bool
	Cwd                string
	WorkspaceID        string
	WorkspaceTitle     string
	WorkspaceTemporary bool
	Model              string
	CreatedAt          string
	UpdatedAt          string
	Origin             string
	Capabilities       *SessionCapabilities
}

// SessionCapabilities 会话的能力选定回显（`nil` 指针 = 该 kind 未声明，与 `[]` 语义相反）。
type SessionCapabilities struct {
	Skills *[]string
	Mcps   *[]string
	// Expert 会话钉住的专家 id（nil = 未声明；`""` = 明确不用；非空 = id）。
	Expert      *string
	Fingerprint string
}

// SessionSearchHitItem 一条会话搜索命中（概要 + 片段与命中数）。
type SessionSearchHitItem struct {
	SessionItem
	Snippet string
	Matches int
}

// RunPhase 运行阶段的进行时快照（`Run` 字段用它）。
type RunPhase struct {
	RunID      string
	Status     string
	Phase      string
	Detail     string
	ToolCallID string
	ToolName   string
	WaitingFor string
	Reason     string
	At         string
	Seq        int64
}

// RunItem 一条运行记录。
type RunItem struct {
	ID           string
	UserText     string
	Status       string
	ErrorCode    string
	StopReason   string
	FirstSeq     int64
	LastSeq      int64
	StartedAt    string
	EndedAt      string
	FirstTokenMS int64
	Usage        *RunUsage
}

// RunUsage 一次运行的用量。
type RunUsage struct {
	Used         int64
	Size         int64
	CostAmount   float64
	CostCurrency string
}
