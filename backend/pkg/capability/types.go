package capability

import "encoding/json"

// ——— 能力数据面领域类型（设计文档 §4.4） ———
//
// 这些类型同时充当磁盘 JSON 定义（目录文件）与内存 Registry 的条目；
// REST 对外表示在 server/handler 层另建（密钥/headers 不读回，见 McpServerResp）。

// 传输与工具来源枚举。
const (
	// MCP server 传输方式（design-and-roadmap v4④ 判别联合子集）。
	TransportStdio          = "stdio"
	TransportSSE            = "sse"
	TransportHTTP           = "http"
	TransportStreamableHTTP = "streamable_http"

	// Tool 来源：平台工具/静态目录、MCP tools/list（P2 live）、观察兜底（tool_call 通知透传）。
	ToolSourceBuiltin  = "builtin"
	ToolSourceMCP      = "mcp"
	ToolSourceObserved = "observed"
)

// Transport 合法值集合（校验用）。
var ValidTransports = map[string]bool{
	TransportStdio: true, TransportSSE: true,
	TransportHTTP: true, TransportStreamableHTTP: true,
}

// ValidToolSources 合法工具来源集合（校验用）。
var ValidToolSources = map[string]bool{
	ToolSourceBuiltin: true, ToolSourceMCP: true, ToolSourceObserved: true,
}

// Skill 是 Skill 目录的一个条目（平台唯一事实源）。
// Body 为 SKILL.md 内容；Sources 描述各 runtime 的物化目标（P2 物化层消费）。
type Skill struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Body        string        `json:"body,omitempty"`
	Sources     []SkillSource `json:"sources,omitempty"`
	// Enabled 启停开关（§13.4 S4）：nil = 未声明（默认启用）；显式 false = 禁用。
	// 用指针是为了「未声明」与「显式关掉」在磁盘上可区分——写回文件时只动它一个键，
	// 其余字段（含未知键）原样保留（见 manager.SetSkillEnabled）。
	Enabled *bool `json:"enabled,omitempty"`
	// Warnings 装载期正文扫描的非 critical 告警（§13.2 S1；critical 已在 RegisterSkill 拒载）。
	// 派生字段：不落盘、不从 JSON 读（`json:"-"`）——磁盘上的定义是事实源，扫描结果每次装载重算，
	// 免得「改了正文但告警还是旧的」。REST 读模型另行暴露（见 SkillResp.Warnings）。
	Warnings []Warning `json:"-"`
}

// IsEnabled 报告 skill 当前是否启用（未声明 = 启用；禁用**只影响投放**，定义与正文照旧保留）。
func (s Skill) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// SkillSource 描述一条 skill 在某个 runtime 原生位置的物化目标。
type SkillSource struct {
	Runtime string `json:"runtime"` // opencode | deepseekharness | ...
	Path    string `json:"path"`    // 该 runtime 的物化路径
	Format  string `json:"format"`  // 该 runtime 的 skill 格式（如 "SKILL.md" / "agents/skills"）
}

// McpServer 是 MCP 注册表的一个条目（平台唯一事实源）。
//
// §13.3 M1：**明文与密钥在类型上分开**。四个字典各司其职，"哪个是秘密"由字段名回答，
// 不靠读值猜：
//
//	Env           明文环境变量（stdio 子进程注入；可展示、可 diff）
//	SecretEnv     密钥型环境变量：**值只能是引用**（vault:… / env:…）
//	Headers       明文 header（非密钥）
//	SecretHeaders 密钥型 header：值只能是引用
//
// 引用在注入前一刻才解析成明文（internal/manager 调 vault），所以定义文件里永远没有密钥本体。
type McpServer struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Transport string   `json:"transport"`         // stdio | sse | http | streamable_http
	Command   string   `json:"command,omitempty"` // stdio 时
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"` // http/sse 时
	// Env 明文环境变量（stdio 注入）。
	Env map[string]string `json:"env,omitempty"`
	// SecretEnv 密钥型环境变量：值必须是 vault:<ns>/<name> 或 env:<NAME>。
	SecretEnv map[string]string `json:"secret_env,omitempty"`
	// Headers 明文 header（非密钥）。
	Headers map[string]string `json:"headers,omitempty"`
	// SecretHeaders 密钥型 header：值必须是 vault:<ns>/<name> 或 env:<NAME>。
	SecretHeaders map[string]string `json:"secret_headers,omitempty"`
}

// Tool 是 Tool 目录的一个条目（读通：MCP tools/list + 静态/平台工具 + 观察兜底）。
// P1 静态/平台工具与观察条目直接注册；MCP tools/list 条目由 P2 live 探测写入。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Source      string          `json:"source"`               // builtin | mcp | observed
	McpServer   string          `json:"mcp_server,omitempty"` // source=mcp 时的归属 server id（agent 可见性过滤依据）
	Schema      json.RawMessage `json:"schema,omitempty"`     // 参数 JSON Schema（原样保真）
}
