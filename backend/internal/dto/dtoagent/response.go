package dtoagent

// AgentResp Agent 的对外表示。
//
// **没有 runtime 字段**：底座由构建期固定（pkg/runtime/build.go），agent 不再声明运行时候选，
// 用户面也就没有可读/可写的 runtime 选择面。运行时身份只在诊断面（AgentDiagnosticsResp.Runtime）
// 如实出现——那是"事实"，不是"选项"。
type AgentResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	State     string `json:"state"`
	IsDefault bool   `json:"is_default"` // 「当前 Agent」（全局默认单例；可切换，见 POST /agents/:id/default）
	// Seed 内置播种/保留前缀 Agent：只读（不可删；name/provider/策略可改）。
	// 由服务端权威判定（历史上前端按 id 前缀猜，与播种 id 改名后不一致）。
	Seed bool `json:"seed"`
	// Mcps MCP server id 白名单（P2 §5.2）。缺省 = 未声明 → 全量按当前底座 transport 过滤；
	// `[]` = 本 agent 禁用 MCP 注入（两者在 wire 上必须可区分，故用指针 + omitempty）。
	Mcps *[]string `json:"mcps,omitempty"`
	// PermissionPolicy 默认权限策略（M2 #7；缺省 = 全 ask，行为与历史一致）。
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
	// SessionCount 绑定该 agent 的会话数（删除影响面：历史保留可读，删除后无法继续提问）。
	SessionCount int `json:"session_count"`
	// DefaultModel 该 agent 的**派生只读**默认模型：`{"<runtime>": "<model_id>"}`。
	//
	// 它**没有写入方**：`GET /agents` 按**构建期底座**与 `models.is_default` 算出（v12 起
	// `models.is_default` 是单一值，键因此恒定只有一个），于是它随 `is_default` **自动变**，
	// 结构上不可能漂移。该底座没有默认模型时不出现在里面。
	//
	// 为什么保留 map 形状而不是收成单值：它是既有对外契约（前端按 runtime 取键），
	// 收窄会连带改模型面的 wire 形状——那是另一件事，不在本次范围。
	DefaultModel map[string]string `json:"default_model,omitempty"`
}

// AgentDeleteResp 删除 agent 的结果与影响面。
//
// 归属不可变：存量会话的 agent_id 不被改写、也不静默改绑到别的 agent（那会连带静默换掉权限面）；
// 用户要换 agent 只能在**新会话**里换。
type AgentDeleteResp struct {
	Deleted string `json:"deleted"`
	// AffectedSessions 绑定该 agent 的会话总数（含归档：删除后均无法继续提问）。
	AffectedSessions int `json:"affected_sessions"`
	// DetachedSessions 本次被摘除的活跃会话数（内存里仍连着的会话立即失效，后续 prompt
	// 报 agent_not_found，而不是「删了还能跑、重启后才失效」）。
	DetachedSessions int `json:"detached_sessions"`
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
	// "read-only"|"workspace-write"|"danger-full-access" 显式指定（当前作用于 dsh）。
	NativeMode string `json:"native_mode,omitempty"`
}

// AgentLastErrorResp 某 agent 最近一次失败的诊断快照。
type AgentLastErrorResp struct {
	At         string   `json:"at"`
	SessionID  string   `json:"session_id,omitempty"`
	Code       string   `json:"code"`
	Class      string   `json:"class"`
	Details    string   `json:"details"`
	StderrTail []string `json:"stderr_tail,omitempty"`
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
