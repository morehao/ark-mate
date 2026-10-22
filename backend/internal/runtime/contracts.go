package runtime

import "github.com/morehao/ark-mate/internal/core/resource"

// Capabilities 是 runtime 的能力基线声明（静态）。
// 真实运行时能力以 ACP initialize 探测结果为准（acp.Capabilities）。
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
	Args    []string // 参数（如 "acp" 这类子命令 / --config 路径；stdio 形态）
	Env     []string // 附加环境（KEY=VALUE，叠加在 os.Environ 之上；stdio 形态）
	// Transport 传输形态："stdio"（默认）| "http"（Streamable HTTP）| "ws"（WebSocket）。
	Transport string
	// Endpoint 远程 ACP 端点 URL（Transport=http|ws 时必填）。
	Endpoint string
	// Headers 远程端点附加请求头（鉴权等；http/ws 均带上）。
	Headers map[string]string
}

// IsolationValueKind 隔离变量的取值形态（三种，互斥）。
type IsolationValueKind string

const (
	// IsolationPath 值 = `<MATE_HOME>/<Path 各段>`：把底座的状态/配置落进平台自己的目录。
	IsolationPath IsolationValueKind = "path"
	// IsolationLiteral 值 = `Value` 字面量（开关类，如 "true"）。
	IsolationLiteral IsolationValueKind = "literal"
	// IsolationEmpty 值 = 空串。语义是**清掉用户 shell 里的同名变量**（JS 侧 `''` 是 falsy，
	// 等价于"没设"），而不是"没配这一条"。**不许**把它翻译成兜底路径：底座会把一个目录当
	// 配置文件读（实测 EISDIR，2026-10-10 桌面态 skill 验收抓到的）。
	IsolationEmpty IsolationValueKind = "empty"
)

// IsolationVar 一条 runtime 子进程的配置隔离声明（投影与语义见 isolation.go）。
//
// 为什么由 adapter 声明而不是父包 switch（本次收口）：变量名是**该底座的私有知识**
// （实测得来：有的底座读 JS 源码、有的对二进制做 `strings`），父包不该认识任何具体底座。
// 声明落在**该 adapter 的 Spec 汇入点**（`adapters.go` 里的 `xxxSpec()`，如 `dshSpec()`，
// 与 `Projections` / `ModelConfig` / `Credential` / `SetupSteps` 同一处），父包只做机械投影，
// 于是"接一个新 runtime"= 新建一个目录 + 加一个 `xxxSpec()`，而不是回来改 frameworks 文件。
//
// 为什么不放进 adapter 实现包（`internal/infra/dsh`）：那会形成父子 import 环（本包要 import adapter
// 才能汇入 Spec，adapter 又 import 父包才能引用本类型）。这是本仓既有的"adapter 子包只提供纯数据与
// 端口实现，父包汇入统一 Spec"约定的直接后果，不是省事。
type IsolationVar struct {
	// Key 环境变量名（子进程侧）。
	Key string
	// Kind 取值形态；决定 Path / Value 里哪一个被使用。
	Kind IsolationValueKind
	// Path 相对 `<MATE_HOME>` 的路径段（Kind=path）。刻意是**段**而不是整串：
	// 拼接只能发生在父包（adapter 拿不到 home，约束⑦），且段形式天然挡住绝对路径逃逸。
	Path []string
	// Value 字面取值（Kind=literal）。
	Value string
	// LauncherFallback 桌面私有启动器（`gen-runtime-bin.mjs`）是否也兜底它。
	//
	// true = 壳直接跑随包载荷时启动器要兜底（该变量是**该底座专属**的落点，没人兜底就会去
	// 读写用户真实目录）；false = **核心专供**（通用变量，如全局 XDG 与 npm 面——壳里改它们会
	// 连累用户其它工具），此时必须在该 adapter 的声明处写明理由。
	//
	// 它取代了 archtest 里那张手工维护的 `launcherCoreOnly` 豁免表：那张表是同一事实的第二份
	// 手抄副本，漏登记就是**静默漏点**；做成声明后守卫可以判"两侧逐条相等"而不是"子集 + 豁免"。
	LauncherFallback bool
}

// Spec 是注册表里一个 runtime 的投影描述（§6.1 ProjectionSpec 的声明式半场：
// 启动形态 + 传输能力 + **纯数据**的落点/凭据/隔离声明；可执行半场 = acp 连接 + 该 adapter 的
// `resource.Syncer`，后者按 ADR-2 挂在 `Registry` 的第一张可执行端口表上，不进 Spec）。
type Spec struct {
	ID      string // 注册表键（= 该 adapter 的 id，如 "dsh"）
	Name    string // 展示名
	Version string // 展示用启动形态描述
	Launch  LaunchSpec
	Caps    Capabilities // 静态能力基线声明
	// Isolation 该 runtime 子进程的**配置隔离声明**（纯数据；语义与投影见 isolation.go）。
	//
	// 它只是**声明**：真正叠加到 `Launch.Env` 是装机期动作（`Registry.ApplyIsolationEnv`），
	// 因为落点要靠 `<MATE_HOME>` 才能拼出来，而 adapter 拿不到 home。
	// 与 `ModelConfig.Root` 的 `${DSH_HOME}` 是同一件事的两面：那边声明"读到哪"，这边声明"写到哪"。
	Isolation []IsolationVar
	// Description 投影描述（§6.1；面向路由/日志/未来 REST 只读面）。
	Description string
	// McpTransports 该 runtime 支持的 MCP 传输（token：stdio|sse|http|streamable_http；
	// 空 = 仅 stdio——ACP 全 agent 必须支持）。P2 注入时据此过滤平台 MCP 子集（§5.2）。
	McpTransports []string
	// ManagesOwnContext 后端自管上下文/工具（对应 §4.2 ManagesOwnContext；§6.2 三投影均 true）。
	ManagesOwnContext bool
	// ModeMapping 统一权限模式 → 该 runtime 原生 mode。实测结论：各底座的权限面形态**各异**
	// （有的是 mode env + approval 预设、有的是逐动作 permission 表，ACP 面上没有 mode 参数），
	// 没有可直接套用的原生词表 → **空 = 有据结论，不臆造**；统一模式语义落平台 permission 面。
	// 这张表与 ModeMapping 的消费者是同一个端口（`NativePermissionAdapter`），本轮在册的实现只
	// 声明 `native_mode`（走端口、不走本表），留空因此是正确的。
	ModeMapping map[string][]string
	// SetupSteps 该底座未就绪时的可执行修复步骤（安装/登录等，来自 adapter 的既有知识）。
	// 消费面：GET /runtime/diagnostics 的 remediation（诊断面据此给出「怎么修」而不是只有报错）。
	SetupSteps []string
	Notes      string // 约束/注意事项（adapter 的既有知识，如不支持哪些 ACP 方法）；经 GET /runtime/diagnostics 外露
	// Projections 该 runtime 承载各类能力的**原生形态**声明（投影契约，见 projection.go）。
	// 空 = 该 runtime 不承载任何平台可投影的能力（远程端点即如此 → not_applicable）。
	Projections []Projection
	// ModelConfig 该 runtime 的**模型清单落点**（见 config_location.go）：**只有位置**，不含格式。
	//
	// 为什么只留位置、删掉格式与通道（ADR-2/ADR-11）：`providers` 复数键、`- id: acp`、`--patch`
	// 这些是**底座的私有知识**，只有写字节的 adapter 知道；留在内核里等于每接一个底座就改内核。
	// 落点之所以还在 `Spec`，是因为 `${VAR}` 这类占位符（如 dsh 的 `${DSH_HOME}`）**只能靠该
	// runtime 自己的隔离 env 解出来**（adapter 拿不到注册表，约束⑦），解析必须发生在父包。
	//
	// 未声明 Filename = 该 runtime 不承载平台模型清单：清单写面按"未声明"拒绝（40501），
	// `GET /models` 回落成只读运行时目录。
	//
	// 与 Projections 分开的理由见 config_location.go 文件头（一个能力一个目录 vs 一个 runtime 一个文件）。
	ModelConfig ModelConfigLocation
	// Credential 该 runtime 的**凭据交付面**声明（纯数据，见 internal/core/resource.CredentialSpec）。
	//
	// 与 ModelConfig 分开：清单与凭据是**两条通道**，生效时刻与失败半径都不同（ADR-4/ADR-14）。
	// 零值 = 该 runtime 没声明凭据面。
	Credential resource.CredentialSpec
	// DeclaresModelInput 该 runtime 的清单交付面是否**声明**模型的输入模态（ADR-12）。
	//
	// true = 平台生成的配置里每个模型条目都带 `input`（dsh 的 `models[].input`）；
	// false = 平台**不写**它，该底座自己知道模型能收什么（若某底座的对应字段是整块替换、多字段必填，
	// 为声明 `input` 就得用猜测连带声明别的字段 ⇒ 那一侧宁可写 false）。
	//
	// 它存在的唯一理由是**如实可读**：用户面与 `mate doctor` 要能区分"已在该底座上声明"与
	// "仅平台记录"——否则用户会以为在那一侧勾了图片也生效了（ADR-12 的结论段）。
	// **它不是门禁**：能不能收图一律走连接期事实（`internal/infra/protocol/acp.Capabilities.Images`，ADR-14）。
	DeclaresModelInput bool
	// ModelCatalogNeedsCredential 该 runtime 的会话模型目录是否**按凭据过滤**。
	//
	// dsh 0.2.1 起 `llm-pi-ai` 的 `listModels` 在 `apiKeyEnv` 解析不到时返回空目录
	// （`deepseek-harness:packages/llm/llm-pi-ai/src/adapter.ts:302`），于是「缺密钥」的表现
	// 从「请求失败」变成「模型不在广告列表里」，而 `session/set_config_option` 会拒绝一个
	// 不在列表里的值。true = 平台在 applyModel 前先给出可执行的缺密钥错误，而不是把底座的
	// invalidParams 原样甩给用户；false = 该底座不按凭据过滤目录。
	ModelCatalogNeedsCredential bool
	// CapabilityScope 该 runtime 的能力面（MCP 集、skill 目录扫描）在哪个粒度被确定：
	// "process"（默认，最粗）| "directory" | "session"。**未实测一律 process**（P-2 的三条纪律）。
	CapabilityScope string
	// Tested 该 CapabilityScope 是否经真实底座实测（false → 回落到最保守的 process 语义）。
	Tested bool
	// TestedOnVersion 实测时的底座版本；版本一变 Tested 失效（绑定范围可能随底座版本变）。
	TestedOnVersion string
}
