package runtime

import "github.com/morehao/ark-mate/pkg/resource"

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
// 启动形态 + 传输能力 + **纯数据**的落点/凭据声明；可执行半场 = acl 连接 + 该 adapter 的
// `resource.Syncer`，后者按 ADR-2 挂在 `Registry` 的第二张表上，不进 Spec）。
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
	// SetupSteps 该底座未就绪时的可执行修复步骤（安装/登录等，来自 adapter 的既有知识）。
	// 消费面：/agents/:id/diagnostics 的 remediation（诊断面据此给出「怎么修」而不是只有报错）。
	SetupSteps []string
	Notes      string // 约束/注意事项（harness 仅 fresh 等，REST 层面暂不外露）
	// Projections 该 runtime 承载各类能力的**原生形态**声明（投影契约，见 projection.go）。
	// 空 = 该 runtime 不承载任何平台可投影的能力（远程端点即如此 → not_applicable）。
	Projections []Projection
	// ModelConfig 该 runtime 的**模型清单落点**（见 config_location.go）：**只有位置**，不含格式。
	//
	// 为什么只留位置、删掉格式与通道（ADR-2/ADR-11）：`providers` 复数键、`- id: acp`、`--patch`
	// 这些是**底座的私有知识**，只有写字节的 adapter 知道；留在内核里等于每接一个底座就改内核。
	// 落点之所以还在 `Spec`，是因为 `${OPENCODE_CONFIG_DIR}` / `${DSH_HOME}` 这类占位符**只能靠该
	// runtime 自己的隔离 env 解出来**（adapter 拿不到注册表，约束⑦），解析必须发生在父包。
	//
	// 未声明 Filename = 该 runtime 不承载平台模型清单：清单写面按"未声明"拒绝（40501），
	// `GET /models` 回落成只读运行时目录。
	//
	// 与 Projections 分开的理由见 config_location.go 文件头（一个能力一个目录 vs 一个 runtime 一个文件）。
	ModelConfig ModelConfigLocation
	// Credential 该 runtime 的**凭据交付面**声明（纯数据，见 pkg/resource.CredentialSpec）。
	//
	// 与 ModelConfig 分开：清单与凭据是**两条通道**，生效时刻与失败半径都不同（ADR-4/ADR-14）。
	// 零值 = 该 runtime 没声明凭据面。
	Credential resource.CredentialSpec
	// CapabilityScope 该 runtime 的能力面（MCP 集、skill 目录扫描）在哪个粒度被确定：
	// "process"（默认，最粗）| "directory" | "session"。**未实测一律 process**（P-2 的三条纪律）。
	CapabilityScope string
	// Tested 该 CapabilityScope 是否经真实底座实测（false → 回落到最保守的 process 语义）。
	Tested bool
	// TestedOnVersion 实测时的底座版本；版本一变 Tested 失效（绑定范围可能随底座版本变）。
	TestedOnVersion string
}

// ListItem 是 /runtimes 列表项的对外表示（由控制面 handler 消费）。
type ListItem struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version string       `json:"version"`
	Caps    Capabilities `json:"capabilities"`
}
