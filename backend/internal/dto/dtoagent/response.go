package dtoagent

// AgentResp Agent 的对外表示（P0 起不含 runtime：路由是隐藏实现细节）。
type AgentResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	State     string `json:"state"`
	IsDefault bool   `json:"is_default"` // 「当前 Agent」（全局默认单例；可切换，见 POST /agents/:id/default）
	// Seed 内置播种/保留前缀 Agent：只读（不可删、runtime 不可改；name/provider/策略可改）。
	// 由服务端权威判定（历史上前端按 id 前缀猜，与播种 id 改名后不一致）。
	Seed bool `json:"seed"`
	// Runtimes 候选 runtime 顺序（ResolveRuntime FirstAvailable 按此试探；0/1 个即确定）。
	// 供前端判定模型作用域（opencode=进程级锁定 / dsh=会话级可改）；空 = 未知。
	Runtimes []string `json:"runtimes,omitempty"`
	// Mcps MCP server id 白名单（P2 §5.2）。缺省 = 未声明 → 全量按 runtime transport 过滤；
	// `[]` = 本 agent 禁用 MCP 注入（两者在 wire 上必须可区分，故用指针 + omitempty）。
	Mcps *[]string `json:"mcps,omitempty"`
	// PermissionPolicy 默认权限策略（M2 #7；缺省 = 全 ask，行为与历史一致）。
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
	// ConversationCount 绑定该 agent 的会话数（删除影响面：历史保留可读，删除后无法继续提问）。
	ConversationCount int `json:"conversation_count"`
}

// AgentDeleteResp 删除 agent 的结果与影响面。
//
// 归属不可变：存量会话的 agent_id 不被改写、也不静默改绑到别的 agent（那会连带静默换掉权限面）；
// 用户要换 agent 只能在**新会话**里换。
type AgentDeleteResp struct {
	Deleted string `json:"deleted"`
	// AffectedConversations 绑定该 agent 的会话总数（含归档：删除后均无法继续提问）。
	AffectedConversations int `json:"affected_conversations"`
	// DetachedConversations 本次被摘除的活跃会话数（内存里仍连着的会话立即失效，后续 prompt
	// 报 agent_not_found，而不是「删了还能跑、重启后才失效」）。
	DetachedConversations int `json:"detached_conversations"`
	// WasDefault 被删的是否为「当前 Agent」（是则回落内置默认并清除 default.json 的显式选择）。
	WasDefault bool `json:"was_default"`
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
	// Remediation 可执行修复步骤（基于事实推导：runtime 未注册/启动命令不可执行/provider 档案
	// 悬空或无密钥/底座自报的鉴权方式/底座 SetupSteps）。健康快照不带该字段。
	Remediation []string `json:"remediation,omitempty"`
	CheckedAt   string   `json:"checked_at"`
}
