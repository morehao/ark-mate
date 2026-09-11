package dtoconversation

// MemoryRevokeReq 撤销权限记忆请求体（title+kind 均空 = 一键撤销全部）。
type MemoryRevokeReq struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// ConversationPatch 会话元数据子集更新（nil/零值不更新）。
type ConversationPatch struct {
	Title    *string `json:"title"`
	Pinned   *bool   `json:"pinned"`
	Archived *bool   `json:"archived"`
	Model    *string `json:"model"` // 会话级模型（opencode 仅创建时可选；dsh 会话内即时）
}

// PromptReq 追加 prompt 请求体（POST /conversations/:id/prompt）。
type PromptReq struct {
	Prompt string `json:"prompt" binding:"required"`
	// Delivery 投递模式（可选）：空/reject = 忙时报 turn_in_progress（历史行为）；
	// after_turn = 排队，当前轮结束后自动投递；interrupt_then_prompt = 取消当前轮随后投递。
	// 非法值显式报 invalid_delivery（不静默回落：投递语义是用户意图，猜错等于丢输入）。
	Delivery string `json:"delivery,omitempty"`
}

// ConversationCreateReq 新建会话请求体。
type ConversationCreateReq struct {
	AgentID string `json:"agent_id"` // 空 = 默认播种 Agent（dsh）
	Prompt  string `json:"prompt"`
	// Cwd 会话工作空间（绝对路径）。空 = 平台代建临时空间（见响应 workspace_temporary）；
	// 非空时校验「绝对 / 存在 / 是目录」，通过后按符号链接规范化落库。
	// 工作空间建成后不可变——它是底座 ACP session 的 cwd，也是原生权限 workspace-write 的边界。
	Cwd string `json:"cwd,omitempty"`
	// Model 运行时目录（GET /models 的 options[category=model].values[].value）里的**不透明取值**，
	// 原样回传（空 = 跟随运行时默认）。
	Model string `json:"model,omitempty"`
}

// HistoryQuery 会话历史分页参数（GET /conversations/:id/history）。
// After/Before 互斥：After 向后（seq > after，升序）；Before 向前（seq < before，仍升序返回，
// 用于「加载更早」）。Limit 1..1000（0 → 默认 200）。
type HistoryQuery struct {
	After int64
	// Before 向前游标（seq < Before）；Before=0 且 HasBefore=true = 取末尾一页。
	Before    int64
	HasBefore bool
	Limit     int
}

// PermissionResolveReq 权限决策请求体。
type PermissionResolveReq struct {
	CallID   string `json:"call_id" binding:"required"`
	OptionID string `json:"option_id"`
	Always   bool   `json:"always"`
	Allowed  bool   `json:"allowed"`
}

// ElicitationResolveReq 结构化提问决策请求体。
type ElicitationResolveReq struct {
	ElicitationID string         `json:"elicitation_id" binding:"required"`
	Action        string         `json:"action" binding:"required"` // accept | decline | cancel
	Content       map[string]any `json:"content,omitempty"`         // accept 时用户提交的表单内容
}
