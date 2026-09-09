package runtime

// Capabilities 是 runtime 的能力基线声明（静态）。
// 真实运行时能力以 ACP initialize 探测结果为准（acl.Capabilities）。
type Capabilities struct {
	SessionsPersistent     bool // 支持 load/list/resume/fork/close 等持久会话
	MultiSessionConcurrent bool // 同连接多会话
	Images                 bool // prompt 可带图
	EmbeddedContext        bool // prompt 可内嵌 Resource
}

// LaunchSpec 描述如何接入该 runtime 的 ACP server。
//
// 两种形态（A5）：
//   - 本地子进程（默认，Transport 为空/"stdio"）：Command/Args/Env 生效，stdio 传输；
//   - 远程端点（Transport="http"|"ws"）：Endpoint/Headers 生效，Command/Args 忽略。
type LaunchSpec struct {
	Command string   // 可执行文件（须在 PATH 中；stdio 形态）
	Args    []string // 参数（如 opencode acp 的子命令/--config 路径；stdio 形态）
	Env     []string // 附加环境（KEY=VALUE，叠加在 os.Environ 之上；stdio 形态）
	// Transport 传输形态："stdio"（默认）| "http"（Streamable HTTP）| "ws"（WebSocket）。
	Transport string
	// Endpoint 远程 ACP 端点 URL（Transport=http|ws 时必填）。
	Endpoint string
	// Headers 远程端点附加请求头（鉴权等；http/ws 均带上）。
	Headers map[string]string
}

// Spec 是注册表里一个 runtime 的投影描述（§6.1 ProjectionSpec 的声明式半场：
// 启动形态 + 传输能力 + 配置投影字段；可执行半场 = acl 连接 + 后续 Runtime 接口）。
type Spec struct {
	ID      string // 注册表键，如 "opencode"、"deepseekharness"
	Name    string // 展示名
	Version string // 展示用启动形态描述
	Launch  LaunchSpec
	Caps    Capabilities // 静态能力基线声明
	// Description 投影描述（§6.1；面向路由/日志/未来 REST 只读面）。
	Description string
	// McpTransports 该 runtime 支持的 MCP 传输（token：stdio|sse|http|streamable_http；
	// 空 = 仅 stdio——ACP 全 agent 必须支持）。P2 注入时据此过滤平台 MCP 子集（§5.2）。
	McpTransports []string
	// ManagesOwnContext 后端自管上下文/工具（对应 §4.2 ManagesOwnContext；§6.2 三投影均 true）。
	ManagesOwnContext bool
	// ModeMapping 统一权限模式（§5.5）→ 该 runtime 原生 mode。实测结论（2026-09-08）：
	// dsh acp 权限=DSH_PERMISSION_MODE env + approval 预设、opencode=per-action permission
	// （ACP 面无 mode 参数），均无与平台四统一模式一一对应的原生词表 → 空 = 有据结论，
	// 不臆造（见 design doc §15）；统一模式语义落平台 permission 面。
	ModeMapping map[string][]string
	// ModelConfigOptionID 模型选择的 config option（如 "model"；空 = 走 env/provider 配置，
	// 对应 §5.5：opencode 走 OPENCODE_MODEL env、dsh 走 provider 配置）。
	ModelConfigOptionID string
	// EffortConfigOptionID 思考强度的 config option（如 "reasoning_effort"；空 = 无）。
	EffortConfigOptionID string
	// ModelEnvKey 模型经启动 env 注入的键（如 "OPENCODE_MODEL"；空 = 模型不经 env，走会话配置）。
	// 仅当 ModelConfigOptionID 为空（模型进程级）时，该 env 并入连接缓存键。
	ModelEnvKey string
	// SetupSteps 首启引导步骤（§6.1 预留；本轮不消费）。
	SetupSteps []string
	Notes      string // 约束/注意事项（harness 仅 fresh 等，REST 层面暂不外露）
}

// ListItem 是 /runtimes 列表项的对外表示（由控制面 handler 消费）。
type ListItem struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version string       `json:"version"`
	Caps    Capabilities `json:"capabilities"`
}
