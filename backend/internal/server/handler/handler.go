package handler

import (
	"context"
	"encoding/json"

	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// Service 面向控制面的领域服务抽象，由上层（如 internal/manager）实现。
// handler 依赖该接口而非具体实现，便于解耦与测试。
type Service interface {
	// ListAgents 返回注册的全部 Agent 概要。
	ListAgents() ([]AgentResp, error)
	// CreateAgent 注册自定义 agent（definitions 持久化；校验 runtime 已注册）。
	CreateAgent(ctx context.Context, req AgentCreateReq) (*AgentResp, error)
	// UpdateAgent 更新 agent 元数据（name/runtime/provider 子集）。
	UpdateAgent(ctx context.Context, id string, patch AgentUpdateReq) (*AgentResp, error)
	// DeleteAgent 删除自定义 agent（种子不可删）。
	DeleteAgent(ctx context.Context, id string) error
	// StartAgent 启动 agent 对应 runtime 连接（校验可启动）。
	StartAgent(ctx context.Context, id string) error
	// StopAgent 停止 agent 对应 runtime 进程（有在途 turn 时拒绝）。
	StopAgent(ctx context.Context, id string) error
	// ListProviders 列出 provider 档案概要（密钥只写不读回）。
	ListProviders() ([]ProviderResp, error)
	// UpdateProvider upsert provider 档案（type/model/key 子集；key 空串 = 清除）。
	UpdateProvider(ctx context.Context, id string, patch ProviderPatchReq) (*ProviderResp, error)
	// ListRuntimes 返回可用 Runtime 概要。
	ListRuntimes() ([]RuntimeResp, error)
	// ListConversations 返回会话概要（可按 archived 过滤）。
	ListConversations(archived bool) ([]ConversationResp, error)
	// SearchConversations 按关键词搜索会话（标题/Agent/正文；含归档；updated 降序截断 limit）。
	SearchConversations(ctx context.Context, q string, limit int) ([]ConversationSearchHit, error)
	// GetConversation 返回单会话详情（含元数据 + 挂起权限）。
	GetConversation(ctx context.Context, id string) (*ConversationDetailResp, error)
	// UpdateConversation 更新会话元数据（title/pinned/archived 子集更新）。
	UpdateConversation(ctx context.Context, id string, patch ConversationPatch) (*ConversationResp, error)

	// CreateConversation 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateConversation(ctx context.Context, req ConversationCreateReq) (*ConversationCreateResp, error)
	// PromptConversation 向既有会话追加一轮 prompt。
	PromptConversation(ctx context.Context, id, prompt string) (*ConversationPromptResp, error)
	// CancelTurn 取消进行中的 turn（无 turn 时幂等 no-op）。
	CancelTurn(ctx context.Context, id string) error
	// ConversationHistory 返回会话的事件历史（eventbus 按聚合回放；after/limit 分页）。
	ConversationHistory(ctx context.Context, id string, after int64, limit int) (*ConversationHistoryResp, error)
	// ListRuns 列出某会话的轮次明细（runs 表读模型：状态/耗时/usage/失败轮回溯；内存模式返回空）。
	ListRuns(ctx context.Context, id string) ([]RunResp, error)
	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]PermissionResp, error)
	// ResolvePermission 按 call_id 决策（allow/reject × once/always）。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) error
	// ListPermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧，供详情/撤销 UI）。
	ListPermissionMemories(ctx context.Context, id string) ([]MemoryResp, error)
	// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
	RevokeMemory(ctx context.Context, id, title, kind string) error
	// ListPendingElicitations 列出某会话的挂起结构化提问（elicitation/create）。
	ListPendingElicitations(ctx context.Context, id string) ([]ElicitationResp, error)
	// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel）。
	ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error

	// ListSkills 返回 Skill 目录（P1 能力数据面）。
	ListSkills() ([]SkillResp, error)
	// ListMcpServers 返回 MCP 注册表（P1；headers/凭证不读回）。
	ListMcpServers() ([]McpServerResp, error)
	// ListTools 返回 Tool 目录（P1；静态/平台/观察条目）。
	ListTools() ([]ToolResp, error)
	// AgentCapabilities 返回某 Agent 的统一能力目录（P2 §8.1：按候选 runtime 聚合 skill/MCP/tool）。
	AgentCapabilities(agentID string) (*AgentCapabilitiesResp, error)
	// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测并写 Tool 目录（P2 §5.2）。
	ProbeMcpServer(ctx context.Context, id string) (*McpProbeResp, error)
}

// AgentResp Agent 的对外表示（P0 起不含 runtime：路由是隐藏实现细节）。
type AgentResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	State     string `json:"state"`
	IsDefault bool   `json:"is_default"` // 默认播种 Agent（前端预选 + 徽标）
	// PermissionPolicy 默认权限策略（M2 #7；缺省 = 全 ask，行为与历史一致）。
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
}

// PermissionPolicyResp 每 agent 默认权限策略对外表示（只读面 + 创建/PATCH 入参）。
type PermissionPolicyResp struct {
	// Default 未匹配到具体工具时的动作：ask | allow | deny（空 = ask）。
	Default string `json:"default,omitempty"`
	// Tools 工具粒度动作：键 = 工具 title 或 ACP kind，值 = ask | allow | deny。
	Tools map[string]string `json:"tools,omitempty"`
}

// AgentCreateReq 注册自定义 agent（对既有 runtime 的命名配置，不写代码即新增）。
type AgentCreateReq struct {
	ID               string                `json:"id" binding:"required"`
	Name             string                `json:"name"`
	Runtimes         []string              `json:"runtimes" binding:"required"`
	Provider         string                `json:"provider,omitempty"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
}

// AgentUpdateReq PATCH agent 元数据（指针字段：nil 不更新）。
type AgentUpdateReq struct {
	Name             *string               `json:"name"`
	Runtimes         *[]string             `json:"runtimes"`
	Provider         *string               `json:"provider"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy"`
}

// ProviderResp provider 档案对外表示（密钥只以 has_key 呈现，绝不读回）。
type ProviderResp struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Model  string `json:"model,omitempty"`
	HasKey bool   `json:"has_key"`
}

// ProviderPatchReq PATCH provider 档案：type/model 指针更新；key 非 nil 即覆盖/清除（空串清除）。
type ProviderPatchReq struct {
	Type  *string `json:"type"`
	Model *string `json:"model"`
	Key   *string `json:"key"`
}

// RuntimeResp Runtime 的对外表示。
type RuntimeResp struct {
	Name string         `json:"name"`
	ID   string         `json:"id"`
	Vers string         `json:"version"`
	Caps []CapabilityKV `json:"capabilities"`
}

// CapabilityKV 单个能力基线（静态声明）的 key/value 表示。
type CapabilityKV struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
}

// ——— P1 能力数据面对外表示（Skill/MCP/Tool 目录；design doc §8.3 能力卡数据源） ———

// SkillResp Skill 目录条目。
type SkillResp struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Body        string            `json:"body,omitempty"` // SKILL.md 内容（P2 物化源）
	Sources     []SkillSourceResp `json:"sources,omitempty"`
}

// SkillSourceResp 一条 skill 在某 runtime 的物化目标。
type SkillSourceResp struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Format  string `json:"format"`
}

// McpServerResp MCP 注册表条目。Headers/访问凭证只存内部（P2 session/new 注入），
// 列表一律不读回（同 ProviderResp 只给 has_key 的立场）。
type McpServerResp struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Transport string   `json:"transport"` // stdio | sse | http | streamable_http
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
	// 以下为 live 探测状态（P2 §5.2；从未探测则零值/空）。
	ToolCount    int    `json:"tool_count"`                 // 该 server 当前归属的 Tool 条目数
	LastProbeAt  string `json:"last_probe_at,omitempty"`    // 最近一次探测时间（RFC3339）
	LastProbeErr string `json:"last_probe_error,omitempty"` // 最近一次探测失败原因（成功则空）
}

// McpProbeResp 一次 MCP live 探测（tools/list）的结果。
type McpProbeResp struct {
	ID            string     `json:"id"`
	Transport     string     `json:"transport"`
	ServerName    string     `json:"server_name,omitempty"`
	ServerVersion string     `json:"server_version,omitempty"`
	ProbedAt      string     `json:"probed_at"`
	Added         int        `json:"added"`      // 本轮写入的 Tool 条目
	Removed       int        `json:"removed"`    // 被本轮替换掉的旧条目
	Skipped       int        `json:"skipped"`    // 非法/无名条目（跳过，不臆造）
	ToolCount     int        `json:"tool_count"` // 探测后该 server 归属条目总数
	Tools         []ToolResp `json:"tools"`
}

// ToolResp Tool 目录条目。
type ToolResp struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Source      string          `json:"source"`               // builtin | mcp | observed
	McpServer   string          `json:"mcp_server,omitempty"` // source=mcp 时的归属 server id（按 agent 可见性过滤）
	Schema      json.RawMessage `json:"schema,omitempty"`     // 参数 JSON Schema（原样保真）
}

// ——— P2 §8.1 统一能力目录（/agents/:id/capabilities） ———

// AgentCapabilitiesResp 某 Agent 视角的统一能力目录：按该 Agent 的候选 runtime 聚合
// 「可物化 / 可注入 / 可目录化」的能力面（管理面只读视图；runtimes 仅作可用性依据，
// 不构成对外路由承诺）。可见性规则（§5.2/§5.4）：
//   - skills：至少一个候选 runtime 有物化源（sources 已按候选 runtime 裁剪）；
//   - mcp_servers：注册表全量 + 每条的 enabled（agent 白名单）与 runtimes（候选里支持该传输的）；
//   - tools：builtin/observed 恒可见；mcp 条目须其归属 server 对该 agent 启用且至少一个候选可承载。
type AgentCapabilitiesResp struct {
	AgentID    string               `json:"agent_id"`
	Name       string               `json:"name,omitempty"`
	Runtimes   []string             `json:"runtimes"`
	Skills     []AgentSkillResp     `json:"skills"`
	McpServers []AgentMcpServerResp `json:"mcp_servers"`
	Tools      []ToolResp           `json:"tools"`
	Notes      []string             `json:"notes,omitempty"` // 裁剪/降级原因（空 = 无特殊情况）
}

// AgentSkillResp 某 Agent 可见的 skill（sources 只保留该 Agent 候选 runtime 的目标）。
type AgentSkillResp struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Sources     []SkillSourceResp `json:"sources,omitempty"`
}

// AgentMcpServerResp 某 Agent 视角的 MCP server。
// Enabled = agent 白名单语义（nil 未声明 → true；显式白名单 → 命中；空数组 → false）。
// Runtimes = 该 Agent 候选中支持此传输的 runtime 子集（空 = 当前无可承载者，不会被注入）。
type AgentMcpServerResp struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Transport string   `json:"transport"`
	Enabled   bool     `json:"enabled"`
	Runtimes  []string `json:"runtimes"`
}

// ConversationResp 会话的对外表示（P0 起不含 runtime）。
type ConversationResp struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Pinned    bool   `json:"pinned"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ConversationSearchHit 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中）。
type ConversationSearchHit struct {
	ConversationResp
	Snippet string `json:"snippet"`
	Matches int    `json:"matches"`
}

// ConversationDetailResp 单会话详情（元数据 + 挂起权限 + 会话级权限记忆）。
type ConversationDetailResp struct {
	ConversationResp
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
}

// ConversationCreateReq 新建会话请求体。
type ConversationCreateReq struct {
	AgentID string `json:"agent_id"` // 空 = 默认播种 Agent（dsh）
	Prompt  string `json:"prompt"`
	Cwd     string `json:"cwd,omitempty"`
}

// ConversationCreateResp 新建会话响应（P0 起不含 runtime）。
type ConversationCreateResp struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // 已发布事件水位（WS attach 续传起点）
}

// ConversationPromptResp 追加 prompt 响应。
type ConversationPromptResp struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // user_message 事件后的水位
}

// ConversationHistoryResp 会话历史分页响应（事件按 seq 升序）。
type ConversationHistoryResp struct {
	ID      string           `json:"id"`
	Events  []eventbus.Event `json:"events"`
	HasMore bool             `json:"has_more"` // 是否还有更早/后续页（客户端用末条 seq 作为下页 after）
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

// PermissionResolveReq 权限决策请求体。
type PermissionResolveReq struct {
	CallID   string `json:"call_id" binding:"required"`
	OptionID string `json:"option_id"`
	Always   bool   `json:"always"`
	Allowed  bool   `json:"allowed"`
}

// ElicitationResp 挂起结构化提问的对外表示。
type ElicitationResp struct {
	ElicitationID string `json:"elicitation_id"`
	SessionID     string `json:"session_id"`
	Mode          string `json:"mode"` // form | url
	Message       string `json:"message"`
	Schema        any    `json:"schema,omitempty"` // form：JSON Schema 对象（UI 据此渲染控件）
	URL           string `json:"url,omitempty"`    // url：引导用户打开的链接
	TimeoutAt     string `json:"timeout_at"`
}

// ElicitationResolveReq 结构化提问决策请求体。
type ElicitationResolveReq struct {
	ElicitationID string         `json:"elicitation_id" binding:"required"`
	Action        string         `json:"action" binding:"required"` // accept | decline | cancel
	Content       map[string]any `json:"content,omitempty"`         // accept 时用户提交的表单内容
}

// Handler 汇总各功能的链式入口（agents / conversations / runtimes / 能力目录 / ws 事件通道）。
type Handler struct {
	Agent        *AgentHandler
	Conversation *ConversationHandler
	Runtime      *RuntimeHandler
	Provider     *ProviderHandler
	Capability   *CapabilityHandler
	WS           *WSHandler
	bus          *eventbus.Bus
	svc          Service
}

// New 组装 Handler，注入领域服务与会话事件总线。
// bus 承载归一化会话事件（架构文档 §2 范式）；WS 事件通道（M0）与后续 manager 事件注入共用它。
func New(svc Service, bus *eventbus.Bus) *Handler {
	return &Handler{
		Agent:        &AgentHandler{svc: svc},
		Conversation: &ConversationHandler{svc: svc},
		Runtime:      &RuntimeHandler{svc: svc},
		Provider:     &ProviderHandler{svc: svc},
		Capability:   &CapabilityHandler{svc: svc},
		WS:           NewWSHandler(bus),
		bus:          bus,
		svc:          svc,
	}
}

// Bus 返回内部事件总线（供 manager 注入真实 ACP 事件，及测试注入）。
func (h *Handler) Bus() *eventbus.Bus { return h.bus }
