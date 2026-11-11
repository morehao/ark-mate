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
	// Model 切换会话的模型：**平台模型行 id**（`mdl_…`；v26 起，此前是运行时目录的不透明取值）。
	// 形状不对或查不到行 → `40401 model_not_found`，**一个字节都不落库**（静默换模型是这一版
	// 最该防的失败）。能改的时机由该底座的能力声明决定。
	Model *string `json:"model"`
}

// SessionCapabilitiesPatch 会话内改能力（`PATCH /sessions/:id/capabilities`）。
//
// **逐 kind 三态**，三个字段都是 `json.RawMessage`：
//
//	键缺失      → nil（不改这一格；gin 的绑定不会给它填值）
//	null        → 置为「未声明」→ 回落上一层（技能/MCP = 默认全量；专家 = 跟随默认）
//	[]          → 明确一个都不要（技能/MCP）
//	["a","b"]   → 白名单（服务端规范化：去空串/去重/升序）
//	""          → 明确不用专家
//	"exp_..."   → 选中该专家
//
// 为什么不用 `*[]string`：它区分不了「键缺失」与「null」（两者都反序列化成 nil），而这两件事
// 一个是"别动"、一个是"改回未声明"——混起来会让"只换技能"顺手清掉专家。同样的手法见
// `SessionCreateReq.AgentID`（那里是为了"出现即拒绝"）。
//
// 语义是**补丁**而不是整体替换：前端只提交被改动的 kind（见 `frontend/.../capabilityEdit.ts`）。
type SessionCapabilitiesPatch struct {
	Skills json.RawMessage `json:"skills"`
	Mcps   json.RawMessage `json:"mcps"`
	Expert json.RawMessage `json:"expert"`
	// Apps 会话启用的 app id 白名单（三态与 Skills/Mcps 同构）：键缺失 = 不改这一格；
	// `null` = 回「未声明」（→ 无 app 工具）；`[]` = 明确一个都不要；非空 = 白名单。
	// 它是会话 app 业务工具面的**唯一来源**（设计 §5.5），也是越权红线的守点。
	Apps json.RawMessage `json:"apps"`
}

// PromptReq 追加 prompt 请求体（POST /sessions/:id/prompt）。
type PromptReq struct {
	Prompt string `json:"prompt" binding:"required"`
	// Delivery 投递模式（可选）：空/reject = 忙时报 run_in_progress（历史行为）；
	// after_run = 排队，当前轮结束后自动投递；interrupt_then_prompt = 取消当前轮随后投递。
	// 非法值显式报 invalid_delivery（不静默回落：投递语义是用户意图，猜错等于丢输入）。
	Delivery string `json:"delivery,omitempty"`
	// Attachments 本轮携带的附件 id 列表（`att_…`；v32 附件链路）。P2 只收不用；
	// P3/P4 起由 sessionruntime 经 AttachmentResolver 解析为 image part / handle 文本。
	Attachments []string `json:"attachments,omitempty"`
	// Skill 本轮一次性技能 id（v4 `/` 技能选择器；如 `plan-first`）。空 = 无技能。
	// 只接受内置技能白名单，非法值显式报 invalid_skill（不静默回落）。
	// 技能契约以隐藏前缀注入本轮 prompt（不进事件/transcript），**仅本轮生效**，一次性消费。
	Skill string `json:"skill,omitempty"`
}

// SessionCreateReq 新建会话请求体。
type SessionCreateReq struct {
	Prompt string `json:"prompt"`
	// AgentID **只用于显式拒绝**（JSON `agent_id`）：agent 概念整个并入 runtime（底座构建期
	// 固定，`internal/runtime/build.go`），"这个会话归哪个 agent"已不是一个可选的东西。
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
	// Model 会话要用的**平台模型行 id**（`mdl_…`；空 = 用平台的默认模型那一行）。
	//
	// v26 起含义是"平台行身份"（**破坏性 wire 变更**，与 v15 同档）：此前它是**运行时目录里的
	// 不透明取值**（原样回传）——那是另一个意思，而键名一直叫 `model`，所以界面看不出来换过。
	// 平台只接受**自己声明过的**模型（可表示性收窄）：形状不对或查不到行 → `40401 model_not_found`。
	// 注意这与"可用性"是两件事：声明过的模型照样可能被底座拒（那时建会话失败，行为一个字不变）。
	// 另注意与 `/models` 行的 `model` **同词不同义**：那边是"发给上游的模型名"。
	Model string `json:"model,omitempty"`
	// Capabilities 会话级能力选定（M3）。**缺省 = 与引入本字段之前逐字节相同**：
	// MCP 全量（按 runtime transport 过滤）、skill 走各自的 runtimes 声明，且不产生能力指纹。
	// 显式传入时逐个校验 id 存在且对该 runtime 可达，不可达即报错并列出原因（不静默丢弃）。
	Capabilities *CapabilitiesReq `json:"capabilities,omitempty"`
	// Attachments 首轮携带的附件 id 列表（`att_…`；v32）。P2 只收不用；P3/P4 起消费。
	// 与 PromptReq.Attachments 同一个词。
	Attachments []string `json:"attachments,omitempty"`
	// Origin 会话来源，**内部字段**（`json:"-"`，不从 wire 读）：'' = 人工，'automation:<id>' = 该
	// automation 触发。不让客户端自称自动化产物——它会给会话列表打标、也是
	// `GET /automations/:id/runs` 的快速路径，能自称就意味着能伪造别人的运行史。
	Origin string `json:"-"`
	// 这里曾有 `RuntimeHint string `json:"-"``（automation 的 target.runtime_hint）：底座由构建期
	// 固定（internal/runtime/build.go），"按会话指定底座"这条路径整体下线——留着它只会让下一个人以为
	// 平台还能按会话切换。automation 定义里若还写着 `target.runtime_hint`，写面会 40001（见
	// `internal/domain/automation` 的闸门），读面按已下线键 WARN 后忽略。
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
	// Expert 会话级专家（0..1，三态）：键缺失 = 不选专家（只有整个 capabilities 缺省时，
	// 工作空间默认才可能带入专家）；`""` = 明确不用；非空 = 专家 id。
	//
	// 用指针区分"没出现这个键"与"出现且为空串"——与 Skills/Mcps 的三态同构。
	Expert *string `json:"expert,omitempty"`
	// Apps 会话启用的 app id 白名单（三态：键缺失=未声明→无 app 工具、`[]`=明确不要、非空=启用）。
	// 与 Skills/Mcps 同构；是 app 业务工具独立注入通道的唯一来源（设计 §5.5）。
	Apps *[]string `json:"apps,omitempty"`
}

// HistoryQuery 会话历史分页参数（GET /sessions/:id/history）。
// After/Before 互斥：After 向后（seq > after，升序）；Before 向前（seq < before，仍升序返回，
// 用于「加载更早」）。Limit 1..1000（0 → 默认 200）。
//
// **游标口径（v21）**：`seq` 是**会话内**位次、0 是合法值（`session.created`），于是 `0`
// 再也不能兼任"没有游标"的哨兵（它曾经就是）。两个游标都用 `*int64` 表达"可缺席"：
//   - `nil` = 该方向没有游标：向后 = 从该会话第一条起；向前 = 从最新往回（取末尾一页）；
//   - `After = &0` = 第 0 条**之后**（跳过它）；`Before = &0` = 第 0 条之前（空页，"已到最早"）。
//
// 为什么用指针而不是 `int64 + HasAfter/HasBefore bool`：后者允许"填了值却忘了置标志"这种
// 组合，而它的表现是**静默**的（从头返回一页，看起来像成功）。指针让"有没有游标"与"游标
// 是多少"成为同一个事实，那种组合根本写不出来。
type HistoryQuery struct {
	After  *int64
	Before *int64
	Limit  int
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
	// Late 回答一条**已经超时转 pending** 的提问（ADR-11）。
	//
	// 它走的不是"投给那一轮"而是"**作为新一轮**的用户消息进去"：那一轮的 ACP 调用在超时那一刻
	// 就已经拿到 `cancel` 返回了，把回答投进一个已关闭的通道，表现是"点了发送、什么都没发生"。
	//
	// 缺省 false：常态是"这一轮还在等"。服务端不会替你猜——传错的话两种结果都很难看
	// （把新回答塞进旧轮 / 把正在等的那条当迟到处理）。
	Late bool `json:"late,omitempty"`
}
