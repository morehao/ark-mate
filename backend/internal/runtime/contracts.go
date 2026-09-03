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

// Spec 是注册表里一个 runtime 的完整描述。
type Spec struct {
	ID      string // 注册表键，如 "opencode"、"deepseekharness"
	Name    string // 展示名
	Version string // 展示用启动形态描述
	Launch  LaunchSpec
	Caps    Capabilities // 静态能力基线声明
	Notes   string       // 约束/注意事项（harness 仅 fresh 等，REST 层面暂不外露）
}

// ListItem 是 /runtimes 列表项的对外表示（由控制面 handler 消费）。
type ListItem struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version string       `json:"version"`
	Caps    Capabilities `json:"capabilities"`
}
