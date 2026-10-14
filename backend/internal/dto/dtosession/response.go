package dtosession

import "github.com/morehao/ark-mate/internal/eventbus"

// SessionResp 会话的对外表示（含 runtime 身份）。
type SessionResp struct {
	// ID 平台会话 id（`ses_<26 位 ULID>`）。**它是唯一的对外会话标识**——URL 路径段、
	// 事件载荷的 `session_id`、前端路由都用它。
	ID string `json:"id"`
	// RuntimeSessionID 原生底座会话 id（`rt_<底座原文>`；'' = 尚未起底座会话）。
	//
	// v15 起它在**平台侧**一律带 `rt_` 壳（`pkg/object/objid`）：这既是"平台 id 与底座 id
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
	Title     string `json:"title"`
	Pinned    bool   `json:"pinned"`
	Archived  bool   `json:"archived"`
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
	WorkspaceTemporary bool   `json:"workspace_temporary"`
	Provider           string `json:"provider,omitempty"` // provider 档案 id（会话级模型选择）
	Model              string `json:"model,omitempty"`    // 会话级模型 id（空 = 底座默认）
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	// Origin 会话来源（平台判定，非用户输入）：空 = 人工创建，`automation:<id>` = 该定时任务触发。
	// 回显的意义与 capabilities 同一条：用户要能看见"这条会话是谁开的"，而不是靠时间去猜。
	Origin string `json:"origin,omitempty"`
	// Capabilities 该会话**钉住的**能力选定 + 指纹（M3）。nil = 未声明 → 全量按 runtime 过滤。
	//
	// 回显的意义：用户要能看见"这个会话当初钉的是哪一套"，而不是靠推断。
	// 指纹是不透明摘要（算法保证密钥值不进指纹），透出去只是为了让"是不是同一套"可判。
	Capabilities *SessionCapabilitiesResp `json:"capabilities,omitempty"`
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
	// Fingerprint 钉住的代际指纹（空 = 未声明能力选择）。
	Fingerprint string `json:"fingerprint,omitempty"`
}

// SessionSearchHit 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中）。
type SessionSearchHit struct {
	SessionResp
	Snippet string `json:"snippet"`
	Matches int    `json:"matches"`
}

// SessionDetailResp 单会话详情（元数据 + 挂起权限 + 会话级权限记忆）。
type SessionDetailResp struct {
	SessionResp
	Pending  []PermissionResp `json:"pending"`
	Memories []MemoryResp     `json:"memories"`
}

// MemoryResp 会话级权限记忆的对外表示（decision 区分始终允许/始终拒绝）。
type MemoryResp struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Decision string `json:"decision"` // allow | reject
	Since    string `json:"since"`    // RFC3339，授予时刻
}

// SessionCreateResp 新建会话响应。
type SessionCreateResp struct {
	ID string `json:"id"`
	// RuntimeID 本次会话落在哪个底座上（构建期固定；回带是为了让客户端不必去拼）。
	RuntimeID string `json:"runtime_id"`
	Status    string `json:"status"`
	EventSeq  int64  `json:"event_seq"` // 已发布事件水位（WS attach 续传起点）
	// Cwd / WorkspaceID / WorkspaceTemporary 定稿后的工作空间、它的归属与"是否平台给的"权威标志；
	// 创建即回读，前端无需二次查询即可显示「在哪里工作」并把会话挂到正确的分组下。
	Cwd                string `json:"cwd,omitempty"`
	WorkspaceID        string `json:"workspace_id,omitempty"`
	WorkspaceTemporary bool   `json:"workspace_temporary"`
}

// SessionPromptResp 追加 prompt 响应。
type SessionPromptResp struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // 直接执行 = user_message 水位；入队 = input_queued 水位
	// Queued/InputID 仅 delivery=after_run|interrupt_then_prompt 且会话正忙时出现：
	// 本次输入已入队，等待当前轮结束投递（可用 DELETE /sessions/:id/inputs/:input_id 撤下）。
	Queued  bool   `json:"queued,omitempty"`
	InputID string `json:"input_id,omitempty"`
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
	// TargetRunID 入队时正在执行的那一轮（审计：用户排在哪一轮后面）。
	TargetRunID string `json:"target_run_id,omitempty"`
	// Generation 入队时会话执行代际（服务端投递前比对，不等即作废）。
	Generation int64  `json:"generation,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"` // RFC3339Nano(UTC)
}

// SessionHistoryResp 会话历史分页响应（事件按 seq 升序）。
type SessionHistoryResp struct {
	ID     string           `json:"id"`
	Events []eventbus.Event `json:"events"`
	// HasMore 沿当前分页方向是否还有更多：after 分页 = 后面还有；before 分页 = 更早还有。
	HasMore bool `json:"has_more"`
	// FirstSeq/LastSeq 本页首尾事件 seq（空页为 0）；客户端据此继续向前/向后翻页。
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
	FirstSeq   int64  `json:"first_seq"`
	LastSeq    int64  `json:"last_seq"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"` // 终态时刻（进行中为空）
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
	TimeoutAt string                 `json:"timeout_at"`
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
	// | ambiguous（两者都无，不归属；见 pkg/acl/scope.go 与 manager/diagnostics.go 的留痕）。
	ScopeSource string         `json:"scope_source"`
	Mode        string         `json:"mode"`
	Message     string         `json:"message"`
	Schema      map[string]any `json:"schema,omitempty"`
	URL         string         `json:"url,omitempty"`
	TimeoutAt   string         `json:"timeout_at"`
}
