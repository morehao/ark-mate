package runtime

// Capabilities 是 runtime 的能力基线声明（静态）。
// 真实运行时能力以 ACP initialize 探测结果为准（acl.Capabilities）。
type Capabilities struct {
	SessionsPersistent     bool // 支持 load/list/resume/fork/close 等持久会话
	MultiSessionConcurrent bool // 同连接多会话
	Images                 bool // prompt 可带图
	EmbeddedContext        bool // prompt 可内嵌 Resource
}

// LaunchSpec 描述如何拉起该 runtime 的 ACP server 子进程（stdio）。
type LaunchSpec struct {
	Command string   // 可执行文件（须在 PATH 中）
	Args    []string // 参数（如 opencode acp 的子命令/--config 路径）
	Env     []string // 附加环境（KEY=VALUE，叠加在 os.Environ 之上）
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
	// ModeMapping 统一权限模式（§5.5）→ 该 runtime 原生 mode。opencode/dsh 精确映射待实测
	// （§6.2/§12 开放项），本轮三投影均留空，不臆造。
	ModeMapping map[string][]string
	// ModelConfigOptionID 模型选择的 config option（如 "model"；空 = 走 env/provider 配置，
	// 对应 §5.5：opencode 走 OPENCODE_MODEL env、dsh 走 provider 配置）。
	ModelConfigOptionID string
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
