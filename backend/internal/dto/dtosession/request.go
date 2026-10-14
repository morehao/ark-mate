package dtosession

import "encoding/json"

// MemoryRevokeReq 撤销权限记忆请求体（title+kind 均空 = 一键撤销全部）。
type MemoryRevokeReq struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// SessionPatch 会话元数据子集更新（nil/零值不更新）。
type SessionPatch struct {
	Title    *string `json:"title"`
	Pinned   *bool   `json:"pinned"`
	Archived *bool   `json:"archived"`
	Model    *string `json:"model"` // 会话级模型（能改的时机由该底座的能力声明决定）
}

// PromptReq 追加 prompt 请求体（POST /sessions/:id/prompt）。
type PromptReq struct {
	Prompt string `json:"prompt" binding:"required"`
	// Delivery 投递模式（可选）：空/reject = 忙时报 run_in_progress（历史行为）；
	// after_run = 排队，当前轮结束后自动投递；interrupt_then_prompt = 取消当前轮随后投递。
	// 非法值显式报 invalid_delivery（不静默回落：投递语义是用户意图，猜错等于丢输入）。
	Delivery string `json:"delivery,omitempty"`
}

// SessionCreateReq 新建会话请求体。
type SessionCreateReq struct {
	Prompt string `json:"prompt"`
	// AgentID **只用于显式拒绝**（JSON `agent_id`）：agent 概念整个并入 runtime（底座构建期
	// 固定，`pkg/runtime/build.go`），"这个会话归哪个 agent"已不是一个可选的东西。
	//
	// 为什么留着这个同名 raw 字段而不是直接删：JSON 绑定默认**丢弃未知键**——删掉它会让
	// `{"agent_id":"x"}` 被静默收下然后什么都不发生，用户以为"我指定了 agent"而实际没生效。
	// 出现即 40001 + 指路（`rejectRemovedAgentSelection`，与 `runtimes`/`default_model` 同一条纪律）。
	AgentID json.RawMessage `json:"agent_id"`
	// Cwd 会话工作空间（绝对路径）。空 = 落在**默认工作空间**（响应 workspace_temporary=true）；
	// 非空时校验「绝对 / 存在 / 是目录」，通过后按符号链接规范化落库。
	// 工作空间建成后不可变——它是底座 ACP session 的 cwd，也是原生权限 workspace-write 的边界。
	Cwd string `json:"cwd,omitempty"`
	// WorkspaceID 选中登记的哪个工作空间（v14；空 = 用默认工作空间）。
	//
	// 与 Cwd 的关系：它是选择器选中一个**有名字、有顺序的登记项**时用的；Cwd 是"直接用绝对
	// 路径"这条老路的。两者同时给出且指向不同目录 → 40001 `workspace_conflict`（**拒绝而不猜**：
	// 猜错的后果是会话跑在用户没打算用的目录里，而那个目录正是沙箱边界）。
	// 给了 WorkspaceID 时 Cwd 可以省：平台按登记的路径定 cwd。
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Model 运行时目录（GET /models 的 options[category=model].values[].value）里的**不透明取值**，
	// 原样回传（空 = 跟随运行时默认）。
	Model string `json:"model,omitempty"`
	// Capabilities 会话级能力选定（M3）。**缺省 = 与引入本字段之前逐字节相同**：
	// MCP 全量（按 runtime transport 过滤）、skill 走各自的 runtimes 声明，且不产生能力指纹。
	// 显式传入时逐个校验 id 存在且对该 runtime 可达，不可达即报错并列出原因（不静默丢弃）。
	Capabilities *CapabilitiesReq `json:"capabilities,omitempty"`
	// Origin 会话来源，**内部字段**（`json:"-"`，不从 wire 读）：'' = 人工，'automation:<id>' = 该
	// automation 触发。不让客户端自称自动化产物——它会给会话列表打标、也是
	// `GET /automations/:id/runs` 的快速路径，能自称就意味着能伪造别人的运行史。
	Origin string `json:"-"`
	// 这里曾有 `RuntimeHint string `json:"-"``（automation 的 target.runtime_hint）：底座由构建期
	// 固定（pkg/runtime/build.go），"按会话指定底座"这条路径整体下线——留着它只会让下一个人以为
	// 平台还能按会话切换。automation 定义里若还写着 `target.runtime_hint`，写面会 40001（见
	// internal/manager/automation.go 的闸门），读面按已下线键 WARN 后忽略。
}

// SkillsPtr / McpsPtr 取字段（nil 安全的取值，供调用方少写一层判空）。
func (c *CapabilitiesReq) SkillsPtr() *[]string {
	if c == nil {
		return nil
	}
	return c.Skills
}

// McpsPtr 见 SkillsPtr。
func (c *CapabilitiesReq) McpsPtr() *[]string {
	if c == nil {
		return nil
	}
	return c.Mcps
}

// CapabilitiesReq 会话级能力选定（`capabilities: {skills?: [id], mcps?: [id]}`）。
//
// 两个字段都是**指针**，用来区分"没传这个 kind"（nil → 回落上一层）与"传了空数组"
// （非 nil 空 → 这一类一个都不要）。gin 的绑定对 `[]string` 无法表达这个区分，
// 所以这里必须用指针；`null` 与缺省同义（都反序列化成 nil）。
type CapabilitiesReq struct {
	Skills *[]string `json:"skills,omitempty"`
	Mcps   *[]string `json:"mcps,omitempty"`
}

// HistoryQuery 会话历史分页参数（GET /sessions/:id/history）。
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
