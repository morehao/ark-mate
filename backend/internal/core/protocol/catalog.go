package protocol

// CommandInfo 一条底座自报可用命令的归一化投影。
//
// **它只是"发现面"**：ACP 规范里命令没有独立的执行方法，执行 = 把命令行原样当
// `session/prompt` 的文本发出去。因此本类型不含任何调用语义，也不代表平台"拥有"这些命令
// ——它们是**底座自报**的。
type CommandInfo struct {
	// Name 命令名（不含前导 '/'）。
	Name string `json:"name"`
	// Description 人类可读说明（agent 可留空）。
	Description string `json:"description,omitempty"`
	// Hint 命令需要参数时底座给出的占位提示；无参命令为空。仅作输入提示，不参与校验。
	Hint string `json:"hint,omitempty"`
}

// McpServer 是传输中性的 MCP server 注入表示（上层由能力数据面映射而来；
// headers 可含凭证——仅经协议 wire 下发，REST 不读回）。
type McpServer struct {
	Name      string
	Transport string // stdio | sse | http | streamable_http
	Command   string // stdio 时
	Args      []string
	// Env stdio 子进程环境变量（调用方已把密钥型绑定解析成明文；这里只负责下发）。
	Env     map[string]string
	URL     string // http/sse 时（streamable_http 亦归 http 子对象）
	Headers map[string]string
}

// SessionInfo 列出底座侧会话时的元信息投影。
type SessionInfo struct {
	ID                    string
	Cwd                   string
	Title                 string
	UpdatedAt             string
	AdditionalDirectories []string
}

// Capabilities 是 initialize 阶段一次性冻结的能力断言（供上层裁剪接口，不随会话变化）。
// 一律以协议自报为准：不按 runtime 名字硬编码行为差异。
//
// 原始 SDK 载荷**不在这里**（那是协议绑定的内部状态）；平台值只保留能作出判定的字段。
type Capabilities struct {
	// SessionsPersistent：支持 load/list/resume/fork 等持久会话能力（harness=false）。
	SessionsPersistent bool
	// MultiSessionConcurrent：同连接可同时存在多个会话。
	MultiSessionConcurrent bool
	// Images：prompt 可携带图片。
	Images bool
	// EmbeddedContext：prompt 可内嵌 Resource 上下文。
	EmbeddedContext bool
	// AuthMethods：agent 声明的鉴权方式（非空则需走 Authenticate 流程）。
	AuthMethods []string

	// —— sessionCapabilities.* 展开：供上层决定「能不能 load/resume/list/close/fork」——
	//
	// 关键区别（历史实测两家）：一家广告 {close,list,resume}、另一家 {close,fork,list,resume}，
	// **两家都不广告 delete** —— 因此不得把「建会话→close」当探针（close 不删持久记录）。
	LoadSession   bool
	SessionClose  bool
	SessionList   bool
	SessionResume bool
	SessionDelete bool
	SessionFork   bool

	// AuthGuidance 鉴权方式的展示信息（空 = agent 未声明鉴权要求）。
	// 用途：运行时没有可用模型（未配凭证）时给用户一条确切的补救路径。
	AuthGuidance []AuthGuidance
}

// AuthGuidance 一条鉴权方式的展示信息。
type AuthGuidance struct {
	// MethodID 传给底座 authenticate 的 methodId。
	MethodID string
	Name     string
	// Description agent 给出的说明。
	Description string
	// Terminal 是否需要在终端交互完成登录。
	Terminal bool
	// Command 可执行命令行；为空且 Terminal=true 时由调用方用 runtime 启动命令补齐。
	Command []string
}
