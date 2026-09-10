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
	// DefaultAgentID 返回默认 agent（面板未显式指定 agent 时，用它取模型目录）。
	DefaultAgentID() string
	// ModelCatalog 返回某 agent 的**运行时派生**模型目录（ACP session config options 投影）。
	// 平台不内置任何模型清单：source=session|resume|cache|none 标注取样来源。
	ModelCatalog(ctx context.Context, agentID string) (*ModelCatalogResp, error)
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
	// ConversationHistory 返回会话的事件历史（sqlite 模式读 events 表，内存模式回放 eventbus；
	// after（向后）/before（向前）互斥游标 + limit 分页）。
	ConversationHistory(ctx context.Context, id string, q HistoryQuery) (*ConversationHistoryResp, error)
	// ListRuns 列出某会话的轮次明细（runs 表读模型：状态/耗时/usage/失败轮回溯；内存模式返回空）。
	ListRuns(ctx context.Context, id string) ([]RunResp, error)
	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]PermissionResp, error)
	// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回实际回执。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*PermissionDecisionResp, error)
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
	// CallMcpTool 调用某 MCP server 的工具（B1：tools/call；独立会话，不做连接池）。
	CallMcpTool(ctx context.Context, id, tool string, req McpToolCallReq) (*McpToolCallResp, error)

	// B2：MCP 资源与提示词（live 拉取，不落盘/不目录化）。
	ListMcpResources(ctx context.Context, id string) (*McpResourcesResp, error)
	ReadMcpResource(ctx context.Context, id, uri string) (*McpResourceReadResp, error)
	ListMcpPrompts(ctx context.Context, id string) (*McpPromptsResp, error)
	GetMcpPrompt(ctx context.Context, id, name string, req McpPromptGetReq) (*McpPromptGetResp, error)

	// UsageSummary 聚合用量统计（M3 首项：runs 计数/时长 + 会话上下文/费用快照）。
	UsageSummary(ctx context.Context, q UsageQueryReq) (*UsageSummaryResp, error)

	// AgentDiagnostics 返回某 agent 的诊断快照（A3：stderr 诊断尾巴外露）。
	AgentDiagnostics(ctx context.Context, id string) (*AgentDiagnosticsResp, error)

	// SearchMessages 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转）。
	SearchMessages(ctx context.Context, q string, limit int) (*MessageSearchResp, error)
	// ExportConversations 批量导出会话（M3 ②：wire transcript/事件事实源 → JSON/文本/Markdown）。
	ExportConversations(ctx context.Context, req ExportReq) (*ExportResp, error)

	// RestErr 把领域错误（acl 分类）映射为 REST 信封错误码（40001/40401/40501/40901/50201…）；
	// 已映射的 gerror.Error 原样透传。handler 必须经此出口，避免裸错误漏成 code=-1。
	RestErr(err error) error
}

// UsageQueryReq 用量统计查询参数（GET /usage/summary）。
type UsageQueryReq struct {
	// From/To 半开区间 [From, To)（RFC3339 或 YYYY-MM-DD；空 = 不限该侧）。
	From string
	To   string
	// AgentID 仅统计该 agent（空 = 全部；未知 agent → 40401）。
	AgentID string
	// GroupBy agent（默认）| day（UTC 日期）。
	GroupBy string
}

// UsageBucketResp 一行用量聚合（group_by=agent → key=agent_id；=day → key=YYYY-MM-DD）。
type UsageBucketResp struct {
	Key           string `json:"key"`
	AgentID       string `json:"agent_id,omitempty"`
	Runs          int    `json:"runs"`
	Completed     int    `json:"completed"`
	Failed        int    `json:"failed"`
	Cancelled     int    `json:"cancelled"`
	Running       int    `json:"running"`
	Conversations int    `json:"conversations"`
	DurationMS    int64  `json:"duration_ms"`
	ContextUsed   int64  `json:"context_used"`
	ContextSize   int64  `json:"context_size"`
	// UsageReported 上报过上下文的会话数；CostReported 上报过费用的会话数（两者分开，不可互相替代）。
	UsageReported int `json:"usage_reported"`
	CostReported  int `json:"cost_reported"`
	// CostAmount/CostCurrency：快照金额与币种；多币种时 amount=0、currency=MIXED、明细走 CostByCurrency。
	CostAmount     float64            `json:"cost_amount"`
	CostCurrency   string             `json:"cost_currency,omitempty"`
	CostByCurrency map[string]float64 `json:"cost_by_currency,omitempty"`
}

// UsageSummaryResp 用量统计响应：总计 + 分组桶 + 生效条件回显。
type UsageSummaryResp struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	GroupBy string `json:"group_by"`
	Source  string `json:"source"` // sqlite | memory（memory 模式无统计，返回 40501）
	// ContextSemantics/CostSemantics 口径：均为「区间末快照」，不是区间增量。
	ContextSemantics string `json:"context_semantics"`
	CostSemantics    string `json:"cost_semantics"`
	// BucketsAdditive 桶之间能否相加：agent 分组可加（每会话唯一归属）；day 分组不可加
	//（跨天会话的累计快照会在多个日期桶重复出现）。
	BucketsAdditive bool              `json:"buckets_additive"`
	Totals          UsageBucketResp   `json:"totals"`
	Buckets         []UsageBucketResp `json:"buckets"`
}

// AgentResp Agent 的对外表示（P0 起不含 runtime：路由是隐藏实现细节）。
type AgentResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	State     string `json:"state"`
	IsDefault bool   `json:"is_default"` // 默认播种 Agent（前端预选 + 徽标）
	// Seed 内置播种/保留前缀 Agent：只读（不可删、runtime 不可改；name/provider/策略可改）。
	// 由服务端权威判定（历史上前端按 id 前缀猜，与播种 id 改名后不一致）。
	Seed bool `json:"seed"`
	// Runtimes 候选 runtime 顺序（ResolveRuntime FirstAvailable 按此试探；0/1 个即确定）。
	// 供前端判定模型作用域（opencode=进程级锁定 / dsh=会话级可改）；空 = 未知。
	Runtimes []string `json:"runtimes,omitempty"`
	// PermissionPolicy 默认权限策略（M2 #7；缺省 = 全 ask，行为与历史一致）。
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
}

// PermissionPolicyResp 每 agent 默认权限策略对外表示（只读面 + 创建/PATCH 入参）。
type PermissionPolicyResp struct {
	// Default 未匹配到具体工具时的动作：ask | allow | deny（空 = ask）。
	Default string `json:"default,omitempty"`
	// Tools 工具粒度动作：键 = 工具 title 或 ACP kind，值 = ask | allow | deny。
	Tools map[string]string `json:"tools,omitempty"`
	// NativeMode 原生权限模式（B5）：""/"auto" 推导；"off" 不注入；
	// "read-only"|"workspace-write"|"danger-full-access" 显式指定（当前作用于 deepseekharness）。
	NativeMode string `json:"native_mode,omitempty"`
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

// ConfigOptionValueResp 一个可选值（运行时配置项候选项，如模型）。
//
// Value 是**不透明字符串**：必须原样回传给创建/PATCH 接口，前端不得解析其内部结构
// （dsh 为 JSON.stringify([provider, model])，opencode 为 "provider/model"）。
type ConfigOptionValueResp struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Group/GroupName 分组（agent 以分组形态下发时才有；opencode 为扁平列表时两字段为空）。
	Group     string `json:"group,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	// Unavailable 平台观测：该值最近被运行时拒绝过（如凭证缺失）。仅作提示，最终以运行时为准。
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ConfigOptionResp 一个会话配置项（ACP session config option 的直投影）。
// Category 取值：model / thought_level / mode / model_config（协议标准语义；前端按它渲染，
// 不得按 option id 匹配——各家 id 不同）。
type ConfigOptionResp struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Category     string                  `json:"category,omitempty"`
	Type         string                  `json:"type"`
	CurrentValue string                  `json:"current_value"`
	Grouped      bool                    `json:"grouped,omitempty"`
	Values       []ConfigOptionValueResp `json:"values"`
}

// AuthGuidanceResp 运行时声明的鉴权方式（空目录时给用户一条确切的补救路径）。
type AuthGuidanceResp struct {
	MethodID    string   `json:"method_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Terminal    bool     `json:"terminal,omitempty"`
	Command     []string `json:"command,omitempty"`
}

// ModelCatalogResp 模型目录响应：**全部来自运行时自报**（ACP session config options），
// 平台不内置任何模型清单。Source 标注取样来源：
//   - session：直接读该 agent 的活跃会话；
//   - resume：恢复该 agent 最近一条历史会话读取（读完即 close，不新建会话）；
//   - cache：内存缓存（TTL 内）；
//   - none：运行时没有可用来源（Options 为空，带 Note/鉴权引导）。
type ModelCatalogResp struct {
	Agent   string `json:"agent"`
	Runtime string `json:"runtime"`
	Source  string `json:"source"`
	// FetchedAt 取样时间（RFC3339；source=cache 时为首次取样时间）。
	FetchedAt string `json:"fetched_at,omitempty"`
	// Options 运行时配置项全量投影（模型目录为主；运行时若还给出别的选项，也一并原样透出，仅作展示）。
	Options []ConfigOptionResp `json:"options"`
	// AuthGuidance 运行时声明的鉴权方式（如 opencode 的终端登录命令）。
	AuthGuidance []AuthGuidanceResp `json:"auth_guidance,omitempty"`
	// Note 空目录/降级说明（人类可读，可直接展示给用户）。
	Note string `json:"note,omitempty"`
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

// McpToolCallReq 一次 MCP 工具调用请求（POST /mcps/:id/tools/:tool/call）。
type McpToolCallReq struct {
	// Arguments 工具入参对象（原样透传；nil/缺省 = 无参数）。
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// TimeoutMS 调用超时（毫秒；0 = 默认 30s，上限 5min）。
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// McpToolCallResp 一次 MCP 工具调用结果。
//
// Content 保持 MCP 联合类型原样（文本/图片/资源链接…）；Text 是文本块拼接的可读摘要；
// IsError 是 **工具级** 失败（服务端明确报告），与传输/协议错误（HTTP 信封 code≠0）区分。
type McpToolCallResp struct {
	ServerID          string            `json:"server_id"`
	Tool              string            `json:"tool"`
	IsError           bool              `json:"is_error"`
	Content           []json.RawMessage `json:"content"`
	Text              string            `json:"text,omitempty"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	ServerName        string            `json:"server_name,omitempty"`
	ServerVersion     string            `json:"server_version,omitempty"`
	DurationMS        int64             `json:"duration_ms"`
}

// ——— B2：MCP 资源与提示词（live 拉取） ———

// McpResourcesResp resources/list（+ resources/templates/list）的实时结果。
// 资源与提示词**不做目录化**（不写 capabilities 目录、不落盘）：它们是 server 的实时视图，
// 且可能随权限/时间变化；每次请求现拉，语义最诚实。
type McpResourcesResp struct {
	ServerID       string                    `json:"server_id"`
	Resources      []McpResourceResp         `json:"resources"`
	Templates      []McpResourceTemplateResp `json:"templates,omitempty"`
	TemplatesError string                    `json:"templates_error,omitempty"`
	ServerName     string                    `json:"server_name,omitempty"`
	ServerVersion  string                    `json:"server_version,omitempty"`
}

type McpResourceResp struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

type McpResourceTemplateResp struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

// McpResourceReadReq resources/read 请求体。
type McpResourceReadReq struct {
	URI string `json:"uri"`
}

// McpResourceReadResp resources/read 结果（text 与 blob 二选一，原样保真）。
type McpResourceReadResp struct {
	ServerID string               `json:"server_id"`
	URI      string               `json:"uri"`
	Contents []McpResourceContent `json:"contents"`
}

type McpResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// McpPromptsResp prompts/list 的实时结果。
type McpPromptsResp struct {
	ServerID   string          `json:"server_id"`
	Prompts    []McpPromptResp `json:"prompts"`
	ServerName string          `json:"server_name,omitempty"`
}

type McpPromptResp struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Arguments   []McpPromptArgumentResp `json:"arguments,omitempty"`
}

type McpPromptArgumentResp struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// McpPromptGetReq prompts/get 请求体（MCP 规定参数值为字符串）。
type McpPromptGetReq struct {
	Arguments map[string]string `json:"arguments,omitempty"`
}

// McpPromptGetResp prompts/get 结果（content 保持 MCP 联合类型原样）。
type McpPromptGetResp struct {
	ServerID    string             `json:"server_id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Messages    []McpPromptMessage `json:"messages"`
}

type McpPromptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
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
	Provider  string `json:"provider,omitempty"` // provider 档案 id（会话级模型选择）
	Model     string `json:"model,omitempty"`    // 会话级模型 id（空 = 底座默认）
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ConversationSearchHit 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中）。
type ConversationSearchHit struct {
	ConversationResp
	Snippet string `json:"snippet"`
	Matches int    `json:"matches"`
}

// AgentLastErrorResp 某 agent 最近一次失败的诊断快照。
type AgentLastErrorResp struct {
	At             string   `json:"at"`
	ConversationID string   `json:"conversation_id,omitempty"`
	Code           string   `json:"code"`
	Class          string   `json:"class"`
	Details        string   `json:"details"`
	StderrTail     []string `json:"stderr_tail,omitempty"`
}

// AgentDiagnosticsResp agent 诊断快照（A3）：连接态 + stderr 尾巴 + 最近失败。
type AgentDiagnosticsResp struct {
	AgentID      string              `json:"agent_id"`
	Name         string              `json:"name"`
	State        string              `json:"state"`
	Runtime      string              `json:"runtime"`
	RuntimeName  string              `json:"runtime_name,omitempty"`
	Command      string              `json:"command,omitempty"`
	Connected    bool                `json:"connected"`
	PID          int                 `json:"pid,omitempty"`
	StderrSource string              `json:"stderr_source"` // live | last_error | none
	StderrTail   []string            `json:"stderr_tail"`
	LastError    *AgentLastErrorResp `json:"last_error,omitempty"`
	CheckedAt    string              `json:"checked_at"`
}

// MessageSearchHit 一条消息级命中（跨会话；seq 为锚点，前端据此滚动定位）。
type MessageSearchHit struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Role           string `json:"role"` // user | assistant
	Seq            int64  `json:"seq"`
	SeqHi          int64  `json:"seq_hi,omitempty"`
	MessageID      int64  `json:"message_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Snippet        string `json:"snippet"`
	CreatedAt      string `json:"created_at"`
}

// MessageSearchResp 消息级搜索响应（source 标明数据面，便于排障与降级提示）。
type MessageSearchResp struct {
	Query   string             `json:"query"`
	Source  string             `json:"source"` // sqlite | transcript
	Hits    []MessageSearchHit `json:"hits"`
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
}

// ExportReq 批量导出请求（ids 上限 50；format 默认 markdown）。
type ExportReq struct {
	IDs    []string `json:"ids" binding:"required"`
	Format string   `json:"format"` // markdown | text | json
}

// ExportFile 单会话导出产物（content 为可直接落盘的正文）。
type ExportFile struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Filename       string `json:"filename"`
	Format         string `json:"format"`
	Events         int    `json:"events"`
	Bytes          int    `json:"bytes"`
	Truncated      bool   `json:"truncated,omitempty"` // 事件数超上限（明确标注，不静默截断）
	Content        string `json:"content"`
}

// ExportResp 批量导出响应（skipped 列出无数据/未知会话，不静默丢弃）。
type ExportResp struct {
	GeneratedAt string       `json:"generated_at"`
	Format      string       `json:"format"`
	Exported    int          `json:"exported"`
	TotalBytes  int          `json:"total_bytes"`
	Skipped     []string     `json:"skipped,omitempty"`
	Files       []ExportFile `json:"files"`
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
	Model    *string `json:"model"` // 会话级模型（opencode 仅创建时可选；dsh 会话内即时）
}

// ConversationCreateReq 新建会话请求体。
type ConversationCreateReq struct {
	AgentID string `json:"agent_id"` // 空 = 默认播种 Agent（dsh）
	Prompt  string `json:"prompt"`
	Cwd     string `json:"cwd,omitempty"`
	// Model 运行时目录（GET /models 的 options[category=model].values[].value）里的**不透明取值**，
	// 原样回传（空 = 跟随运行时默认）。
	Model string `json:"model,omitempty"`
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

// ConversationHistoryResp 会话历史分页响应（事件按 seq 升序）。
type ConversationHistoryResp struct {
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

// RunUsageResp 单轮用量快照，与 acl.UsageInfo 同构：used/size 为上下文窗口占用（快照，非增量），
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
	// ConversationID 归属会话（C3 scope：与 session_id 同值；决策必须由该会话发起）。
	ConversationID string `json:"conversation_id"`
	// ScopeSource 归属来源：wire | inferred_inflight（SDK 未建模 scope 时由宿主按唯一在途 turn 推断）。
	ScopeSource string         `json:"scope_source"`
	Mode        string         `json:"mode"`
	Message     string         `json:"message"`
	Schema      map[string]any `json:"schema,omitempty"`
	URL         string         `json:"url,omitempty"`
	TimeoutAt   string         `json:"timeout_at"`
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
	Model        *ModelsHandler
	Capability   *CapabilityHandler
	Usage        *UsageHandler
	Search       *SearchHandler
	Export       *ExportHandler
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
		Model:        &ModelsHandler{svc: svc},
		Capability:   &CapabilityHandler{svc: svc},
		Usage:        &UsageHandler{svc: svc},
		Search:       &SearchHandler{svc: svc},
		Export:       &ExportHandler{svc: svc},
		WS:           NewWSHandler(bus),
		bus:          bus,
		svc:          svc,
	}
}

// Bus 返回内部事件总线（供 manager 注入真实 ACP 事件，及测试注入）。
func (h *Handler) Bus() *eventbus.Bus { return h.bus }
