package dtocapability

import "encoding/json"

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
