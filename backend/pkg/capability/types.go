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
//
// Body 为 SKILL.md 内容；**Runtimes 只表达「给谁用」，不表达「放哪、什么格式」**——后者是
// runtime 的投影声明（见 `pkg/runtime.Projection`）。这也是删掉旧 `Sources` 的原因：
// 条目侧写 path/format 等于让用户为每个底座抄一遍它自己的目录约定，而且两处声明同一件事
// 必然要引入优先级规则。
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Body        string   `json:"body,omitempty"`
	// Runtimes 适用 runtime 白名单（如 ["opencode","deepseekharness"]）；
	// **空 = 全部**支持 skill 投影的 runtime（未声明即不限制）。
	Runtimes []string `json:"runtimes,omitempty"`
	// Scope 作用域：global（默认，写到 runtime 全局目录）/ workspace（写到会话 cwd）。
	// **M1 只落数据面语义，不消费**：workspace 的落点取决于底座原生约定是否会把平台产物混进
	// 用户 git 仓库（假设 B），在实测有结论前不猜路径；声明 workspace 的定义当前按 global 处理。
	Scope string `json:"scope,omitempty"`
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

// 合法 scope 集合（Skill.Scope）。
const (
	SkillScopeGlobal    = "global"
	SkillScopeWorkspace = "workspace"
)

// ValidSkillScopes 合法作用域集合（装载期校验用；未知取值告警跳过，不静默当 global）。
var ValidSkillScopes = map[string]bool{SkillScopeGlobal: true, SkillScopeWorkspace: true}

// EffectiveScope 生效作用域（未声明 = global；非法取值在装载期已被拒，到这里只剩合法值）。
func (s Skill) EffectiveScope() string {
	if s.Scope == "" {
		return SkillScopeGlobal
	}
	return s.Scope
}

// AppliesToRuntime 报告该 skill 是否投放到某 runtime（Runtimes 空 = 全部）。
//
// 「空 = 全部」是刻意的默认：存量定义迁移前没有 runtimes 字段，若空表示"谁都不用"，
// 升级那一刻所有技能会集体从底座目录里被撤回——最坏的一种静默破坏。
func (s Skill) AppliesToRuntime(runtime string) bool {
	if len(s.Runtimes) == 0 {
		return true
	}
	for _, r := range s.Runtimes {
		if r == runtime {
			return true
		}
	}
	return false
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
