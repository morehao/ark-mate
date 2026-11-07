package dtosession

import "github.com/morehao/ark-mate/internal/core/eventbus"

// SessionResp 会话的对外表示（含 runtime 身份）。
type SessionResp struct {
	// ID 平台会话 id（`ses_<26 位 ULID>`）。**它是唯一的对外会话标识**——URL 路径段、
	// 事件载荷的 `session_id`、前端路由都用它。
	ID string `json:"id"`
	// RuntimeSessionID 原生底座会话 id（`rt_<底座原文>`；'' = 尚未起底座会话）。
	//
	// v15 起它在**平台侧**一律带 `rt_` 壳（`internal/core/object/objid`）：这既是"平台 id 与底座 id
	// 形状可分"这条不变式的载体（`id` 匹配 `^ses_…$`、本字段匹配 `^rt_.+$`，两者永不可能
	// 相等），也是排障时一眼看出"这是哪个底座用什么形状发的"的那一层信息。
	//
	// 它**只**用于诊断与关联底座日志：**不得**出现在 URL 路径段，也不得被当成会话标识传给任何写面。
	RuntimeSessionID string `json:"runtime_session_id,omitempty"`
	// RuntimeID 会话归属的底座 id（v19 前这里是 `agent_id`）。
	//
	// 底座由构建期固定，所以它在**今天**恒等于 `GET /meta` 的 `runtime.id`；但它是**会话侧的
	// 执行事实**（历史会话可能停在别的底座上），读面照读不照推。
	RuntimeID string `json:"runtime_id"`
	Status    string `json:"status"`
	// Run 当前轮次的**过程状态**（`run.status` 的读面投影，ADR-6）。列表行与详情都带它——
	// 侧栏会话行的阶段文案（「等待你授权」「正在执行 Bash」）是它的**第二个消费者**，
	// 只给详情会让侧栏永远只有状态点。
	//
	// 没有过过程事件（历史会话 / 本轮还没开始）→ 整个键缺席，**不回空壳**：
	// `{status:""}` 会让界面渲染一个"有状态但为空"的东西。
	Run      *RunPhaseResp `json:"run,omitempty"`
	Title    string        `json:"title"`
	Pinned   bool          `json:"pinned"`
	Archived bool          `json:"archived"`
	// Cwd 会话工作空间（canonical 绝对路径；已随会话创建定稿，之后不可变）。
	// 同时是底座 session 的 cwd 与原生权限 workspace-write 的边界。
	Cwd string `json:"cwd,omitempty"`
	// WorkspaceID 归属的工作空间登记 id（v14）。**总是有值**：未登记/登记已删的会话在
	// 读面归默认工作空间，所以这里下发的是解析后的归属，不是库里的原始列。
	//
	// 由服务端下发而不是让前端按 cwd 匹配：默认工作空间的路径可配置（MATE_WORKSPACE_DIR），
	// 前端拿 cwd 字符串去比登记表会因规范化差异（符号链接、尾斜杠）匹配不上，出现
	// "会话明明在该目录下却不在分组里"（设计 §4.3）。
	WorkspaceID string `json:"workspace_id,omitempty"`
	// WorkspaceTitle 归属工作空间的展示名（v14；空标题已按末级目录名回落，
	// 默认工作空间读作「默认工作空间」）。前端不必再查一次登记表就能渲染分组行。
	WorkspaceTitle string `json:"workspace_title,omitempty"`
	// WorkspaceTemporary 该工作空间是否由**平台**给出（= 默认工作空间）。
	// **权威标志**：调用方据此判断"这个目录不是你选的"，不得从路径形状反推（前缀可配置）。
	//
	// v14 起语义从"平台按会话代建的一次性目录"变成"这是共享的默认工作空间"（设计 ADR-9）：
	// wire key 与字段名保留是因为前端两处按它渲染，换名是纯破坏且没有信息增益。
	WorkspaceTemporary bool `json:"workspace_temporary"`
	// Model 会话引用的**平台模型行 id**（`mdl_…`；空 = 由平台默认/底座默认决定）。
	//
	// v26 起含义是"平台行身份"（**破坏性 wire 变更**，与 v15 同档）：此前它是**运行时目录里的
	// 不透明取值**（原样回传）——那是另一个意思，而键名一直叫 `model`，所以界面看不出来换过。
	// 注意与 `/models` 行的 `model` **同词不同义**：那边是"发给上游的模型名"，这边是平台行 id。
	//
	// 这里曾有 `Provider string`（恒空，v19 后无写入点）；v26 连列一起删了。
	Model string `json:"model,omitempty"`
	// ModelName 会话引用模型的**展示名**（`models.name`，空则回落上游模型名；空 = 未引用，
	// 或那一行已不在平台清单里）。
	//
	// 与 `workspace_title` 同一条口径：读面解析后下发。前端为了"显示一个名字"不必再拉
	// `GET /models`，更不该把 `model`（平台行 id）直接摊到界面上。
	ModelName string `json:"model_name,omitempty"`
	CreatedAt int64  `json:"created_at"` // Unix 毫秒
	UpdatedAt int64  `json:"updated_at"`
	// Origin 会话来源（平台判定，非用户输入）：空 = 人工创建，`automation:<id>` = 该定时任务触发。
	// 回显的意义与 capabilities 同一条：用户要能看见"这条会话是谁开的"，而不是靠时间去猜。
	Origin string `json:"origin,omitempty"`
	// Capabilities 该会话**钉住的**能力选定 + 指纹（M3）。nil = 未声明 → 全量按 runtime 过滤。
	//
	// 回显的意义：用户要能看见"这个会话当初钉的是哪一套"，而不是靠推断。
	// 指纹是不透明摘要（算法保证密钥值不进指纹），透出去只是为了让"是不是同一套"可判。
	Capabilities *SessionCapabilitiesResp `json:"capabilities,omitempty"`
	// NativeMode 会话钉住的原生权限模式（v29）：`read-only` / `workspace-write` /
	// `danger-full-access`；空 = 跟随工作空间默认。只读回显——本轮（D11）会话内不可切，
	// 界面据此显示真实档位（设计 §3.3 P0）。空值 omitempty 缺键，老客户端零影响。
	NativeMode string `json:"native_mode,omitempty"`
}

// SessionCapabilitiesResp 会话能力选定的对外表示。
//
// **形态与落库的 `capability_json` 逐键一致**（回显就是"当初钉住的那一份"），因此三个状态
// 各有明确表示，不需要额外的布尔标志：
//
//	整个对象缺失  未声明能力选择 → 全量按 runtime 过滤 / 各自的 runtimes 声明
//	某个 kind 缺键 该 kind 未声明 → 回落上一层（全量）
//	某个 kind 为 [] **明确一个都不要**（不是"全部"）
//	某个 kind 非空  白名单
//
// 用 `omitempty` 让"未声明"表现为**键缺失**而不是 `null`：`null` 与缺失在语义上无法区分，
// 而这里恰恰要靠这个区分说话。前端取值一律 `capabilities?.mcps`（缺 key = 未声明）。
type SessionCapabilitiesResp struct {
	Skills *[]string `json:"skills,omitempty"`
	Mcps   *[]string `json:"mcps,omitempty"`
	// Expert 会话钉住的专家 id（nil = 未声明；`""` = 明确不用；非空 = id）。
	Expert *string `json:"expert,omitempty"`
	// ExpertMissing 专家定义已被删除（能力仍按创建时物化的列表生效）——读面据此显示「专家已删除」。
	ExpertMissing bool `json:"expert_missing,omitempty"`
	// Warnings 仅**写面响应**可能带（创建、会话内改能力）：专家推荐里被跳过的不可达项
	// （不含密钥值，只含键名与原因），以及「专家人格只在首轮注入」这类本次编辑的边界提示。
	// 读面（GET /sessions/:id）不复现它——那是某次写动作那一刻的事实，不是会话的持久属性。
	Warnings []CapabilityWarningResp `json:"warnings,omitempty"`
	// Fingerprint 钉住的代际指纹（空 = 未声明能力选择）。
	Fingerprint string `json:"fingerprint,omitempty"`
}

// CapabilityWarningResp 一条「专家推荐被跳过」的告警（kind = skill|mcp|model）。
type CapabilityWarningResp struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SessionSearchHit 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中）。
type SessionSearchHit struct {
	SessionResp
	Snippet string `json:"snippet"`
	Matches int    `json:"matches"`
}

// SessionDetailResp 单会话详情（元数据 + 挂起权限 + 会话级权限记忆 + 当前轮次过程）。
type SessionDetailResp struct {
	SessionResp
	Pending  []PermissionResp `json:"pending"`
	Memories []MemoryResp     `json:"memories"`
}

// RunPhaseResp 会话**当前轮次过程**的对外表示（`run.status` 的投影，不是 `runs` 表的行）。
//
// 与 `RunResp`（`GET /sessions/:id/runs` 的轮次明细）的分工：那是**历史账本**（一轮一行，
// 含用量/首令牌耗时/窗口位次），这是**此刻在做什么**（可能同一轮反复变化）。两者共用
// `run_id` 这一支身份，所以界面能对上「头栏说的这一轮」就是「账本里的那一行」。
type RunPhaseResp struct {
	RunID string `json:"run_id,omitempty"`
	// Status 10 个活体取值之一，或恢复收口的 `interrupted`（实时路径永不发后者）。
	Status string `json:"status"`
	// Phase 底座给的更细阶段名，**原样透传**（平台不翻译、不重排；缺失时不出现该键）。
	Phase string `json:"phase,omitempty"`
	// Detail 人话旁白（平台自己的判定产物）。
	Detail string `json:"detail,omitempty"`
	// ToolCallID / ToolName 正在执行的工具（`tool_executing` / `waiting_for_permission` 时有值）。
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	// WaitingFor 在等什么：permission | elicitation | model | tool。
	WaitingFor string `json:"waiting_for,omitempty"`
	// Reason 仅恢复收口带（`restart`）：界面据此说「上一轮因进程重启中断，可重发」，
	// **不**说「失败」——那是底座的结论。
	Reason string `json:"reason,omitempty"`
	// At 该状态事件的时刻（Unix 毫秒）。
	At int64 `json:"at,omitempty"` // Unix 毫秒
	// Seq 该事件在会话内的位次（与 `events.seq` 同一套；界面拿它判断新鲜度与续传）。
	Seq int64 `json:"seq"`
}

// MemoryResp 会话级权限记忆的对外表示（decision 区分始终允许/始终拒绝）。
type MemoryResp struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Decision string `json:"decision"` // allow | reject
	Since    int64  `json:"since"`    // Unix 毫秒，授予时刻
}

// SessionCreateResp 新建会话响应。
type SessionCreateResp struct {
	ID string `json:"id"`
	// RuntimeID 本次会话落在哪个底座上（构建期固定；回带是为了让客户端不必去拼）。
	RuntimeID string `json:"runtime_id"`
	Status    string `json:"status"`
	EventSeq  int64  `json:"event_seq"` // 该会话已发布事件水位（WS attach 续传起点；-1 = 尚无事件）
	// Cwd / WorkspaceID / WorkspaceTemporary 定稿后的工作空间、它的归属与"是否平台给的"权威标志；
	// 创建即回读，前端无需二次查询即可显示「在哪里工作」并把会话挂到正确的分组下。
	Cwd                string `json:"cwd,omitempty"`
	WorkspaceID        string `json:"workspace_id,omitempty"`
	WorkspaceTemporary bool   `json:"workspace_temporary"`
	// Capabilities 本次创建钉住的能力选定（专家 + 物化后的具体技能/MCP + 被跳过的推荐告警）。
	// nil = 请求未声明能力选择（缺省路径，与引入本字段之前逐字节相同）。
	Capabilities *SessionCapabilitiesResp `json:"capabilities,omitempty"`
}

// SessionPromptResp 追加 prompt 响应。
type SessionPromptResp struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // 直接执行 = user_message 水位；入队 = input_queued 水位（会话内位次）
	// Queued/InputID 仅 delivery=after_run|interrupt_then_prompt 且会话正忙时出现：
	// 本次输入已入队，等待当前轮结束投递（可用 DELETE /sessions/:id/inputs/:input_id 撤下）。
	Queued  bool   `json:"queued,omitempty"`
	InputID string `json:"input_id,omitempty"`
}

// QueuedInputMoveReq `POST /sessions/:id/inputs/:input_id/move` 的请求体（DOM insertBefore 语义）。
type QueuedInputMoveReq struct {
	// BeforeID 锚点队项；空 = 追加到末尾。锚点不在该会话的待投递集里 → 40001。
	BeforeID string `json:"before_id,omitempty"`
}

// ComposerSkillResp 一条可供 composer `/` 选择器选择的内置技能（v4 P1）。
// 与「能力数据面」的 `GET /skills`（技能定义目录）是**两回事**：这里是平台内置的、
// 一次性注入的协作技能（如 `plan-first`），无定义文件、无物化、不进指纹。
type ComposerSkillResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// ComposerSkillsResp `GET /composer/skills` 的响应：内置技能目录（顺序即展示顺序）。
type ComposerSkillsResp struct {
	Items []ComposerSkillResp `json:"items"`
}

// QueuedInputResp 一条待投递的排队输入（§6）。
//
// 与事件的关系：`session.input_queued` 是它**曾经入队**的事实，本读模型是它**此刻还在队里**
// 的当前态（投递/撤下后即消失）。前端冷挂载（刷新/直达）时 WS 只发 snapshot 不回放历史，
// 光靠事件会让队列在刷新后凭空消失，故需要这个「当前态」入口把队列查回来。
type QueuedInputResp struct {
	ID       string `json:"input_id"`
	Text     string `json:"text"`
	Delivery string `json:"delivery"` // after_run | interrupt_then_prompt
	// Attachments 本轮携带的附件 id 列表（v4 P2；忙时排队不再丢附件）。
	Attachments []string `json:"attachments,omitempty"`
	// TargetRunID 入队时正在执行的那一轮（审计：用户排在哪一轮后面）。
	TargetRunID string `json:"target_run_id,omitempty"`
	// Generation 入队时会话执行代际（服务端投递前比对，不等即作废）。
	Generation int64 `json:"generation,omitempty"`
	CreatedAt  int64 `json:"created_at,omitempty"` // Unix 毫秒（0 = 缺省）
}

// QueuedInputEditReq `PATCH /sessions/:id/inputs/:input_id` 的请求体（v4 行内编辑）。
// 空 text 视为不合法（编辑必须有正文）；attachments 缺省 = 不携带附件（用空数组表示清空）。
type QueuedInputEditReq struct {
	Text        string   `json:"text" binding:"required"`
	Attachments []string `json:"attachments,omitempty"`
}

// SessionHistoryResp 会话历史分页响应（事件按 seq 升序）。
type SessionHistoryResp struct {
	ID     string           `json:"id"`
	Events []eventbus.Event `json:"events"`
	// HasMore 沿当前分页方向是否还有更多：after 分页 = 后面还有；before 分页 = 更早还有。
	HasMore bool `json:"has_more"`
	// FirstSeq/LastSeq 本页首尾事件 seq（**空页为 -1**；v21 起 0 是合法位次，"空"与"第 0 条"必须可分）；
	// 客户端据此继续向前/向后翻页。
	FirstSeq int64 `json:"first_seq"`
	LastSeq  int64 `json:"last_seq"`
}

// RunResp 轮次明细的对外表示（runs 表读模型：失败轮输入/重试/统计回溯）。
type RunResp struct {
	ID         string `json:"id"`
	UserText   string `json:"user_text"`
	Status     string `json:"status"` // running | completed | failed | cancelled
	ErrorCode  string `json:"error_code,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	// FirstSeq/LastSeq 本轮事件窗口的**会话内**位次（`[first_seq, last_seq]` 闭区间）。
	// 进行中的轮次（v22 起在库里就有行）：`first_seq` 已定（= 本轮 `run.started` 的位次）、
	// `last_seq` 为 **-1**（尚未收口——0 是合法位次，不能当"未知"用）、`ended_at` 为空。
	FirstSeq  int64 `json:"first_seq"`
	LastSeq   int64 `json:"last_seq"`
	StartedAt int64 `json:"started_at"`         // Unix 毫秒
	EndedAt   int64 `json:"ended_at,omitempty"` // 终态时刻（进行中为空）
	// FirstTokenMS 本轮从用户消息到首个 assistant 输出的耗时（ms；未记录/无输出为 0）。
	FirstTokenMS int64 `json:"first_token_ms,omitempty"`
	// Usage 该轮结束后的上下文/费用快照（解析 runs.usage_json；无上报 → nil）。
	Usage *RunUsageResp `json:"usage,omitempty"`
}

// RunUsageResp 单轮用量快照，与 objusage.UsageInfo 同构：used/size 为上下文窗口占用（快照，非增量），
// cost 为会话累计费用（可选）。
type RunUsageResp struct {
	Used         int64   `json:"used"`
	Size         int64   `json:"size"`
	CostAmount   float64 `json:"cost_amount,omitempty"`
	CostCurrency string  `json:"cost_currency,omitempty"`
}

// PermissionResp 挂起权限请求的对外表示。
type PermissionResp struct {
	CallID    string                 `json:"call_id"`
	SessionID string                 `json:"session_id"`
	Title     string                 `json:"title"`
	ToolCall  string                 `json:"tool_call_id"`
	Kind      string                 `json:"kind"`
	Input     any                    `json:"input,omitempty"`
	Options   []PermissionOptionResp `json:"options"`
	TimeoutAt int64                  `json:"timeout_at"` // Unix 毫秒
}

// PermissionOptionResp 权限可选项的对外表示。
type PermissionOptionResp struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionDecisionResp 权限决策回执：描述**实际投递给 agent** 的决策与 ACP 回执形态
// （不是请求参数回显——option_id 可能被兜底为第一个选项，或显式拒绝因无 deny 选项降级 cancelled）。
type PermissionDecisionResp struct {
	CallID           string `json:"call_id"`
	Allowed          bool   `json:"allowed"`
	Rejected         bool   `json:"rejected"`
	Always           bool   `json:"always"`
	OptionID         string `json:"option_id"`
	Outcome          string `json:"outcome"`
	SelectedOptionID string `json:"selected_option_id"`
}

// ElicitationResp 挂起结构化提问的对外表示。
type ElicitationResp struct {
	ElicitationID string `json:"elicitation_id"`
	// SessionID 归属会话（C3 scope；决策必须由该会话发起）。
	//
	// v15 之前这里同时发 `session_id` 与 `conversation_id` 两个**同值**的键（一个东西两个
	// 词），改名后收敛成一个键——两个词各写一遍只会在读的一侧留下一个永远取不到的字段。
	SessionID string `json:"session_id"`
	// ScopeSource 归属来源：wire（`_meta` 提供）| inferred_inflight（宿主按唯一在途 run 推断）
	// | ambiguous（两者都无，不归属；见 internal/infra/protocol/acp/scope.go 与 manager/diagnostics.go 的留痕）。
	ScopeSource string         `json:"scope_source"`
	Mode        string         `json:"mode"`
	Message     string         `json:"message"`
	Schema      map[string]any `json:"schema,omitempty"`
	URL         string         `json:"url,omitempty"`
	TimeoutAt   int64          `json:"timeout_at"` // Unix 毫秒
}
