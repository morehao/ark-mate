// Package objdelivery 承载「应用层资源 → 底座生效方式」的共享值对象与**静态交付矩阵**（M6）。
//
// 为什么单独成包：这套形状同时被三层使用——传输层（`internal/dto/dtomodel` 把它直接作为
// 响应载荷、`internal/dto/dtolibrary` 的能力条目复用它）、实现层（`internal/manager` 逐 kind
// 组装 delivery/applicability），以及模型面（M5 的清单回执）。放在其中任何一层都会让另外两层
// 反向依赖该层；而 `pkg/` 不得 import `internal/`（`internal/archtest` 守卫），所以它只能待在
// `pkg/object` 下，三方都只依赖值对象。
//
// 本包里的交付矩阵是**静态事实表**：**通道按资源种类固定，不随 runtime 变**——provider 密钥走
// 子进程 env（或该底座的原生文档，见 `resource.CredentialSpec`）、会话级模型走会话配置项、
// 模型清单走「写配置文件」。
// 把**通道**做成 per-runtime 声明会创造一个今天不存在的自由度。唯一 per-runtime 的是 T5
// （`config_write`）的**落点**——写哪个文件、什么 schema、是否支持热更——那由
// `pkg/runtime.Spec.ConfigProjection`（`Root`/`Filename`/`Format`/`Effect`）细化，
// 默认取最保守的 `next_process`；本表只声明**通道**与**默认生效时刻**。
//
// 「每个 kind 都必须有声明」由 `internal/archtest/delivery_matrix_test.go` 的
// `TestDeliveryMatrixCoversKinds` 钉住：它从 `internal/persistence/model/enum.go` 的 `DefinitionKind*`
// 常量读出「平台拥有的资源种类」，逐个断言 `For` 查得到声明，并断言未知 kind 必须返回 false。
// 它挡的是「新增一种资源却漏了交付通道声明」这种静默缺口——那种缺口的表现是界面照常显示
// 「已保存」，而用户永远等不到它生效，且没有任何一处会报错。
package objdelivery

import "slices"

// Delivery 一类资源 → 底座的交付声明：走哪条**通道**、什么时候**生效**、一句人话说明。
//
// 三个字段都要如实：`Note` 是**可执行的一句话**（说清改完之后到底会发生什么、要等到哪一刻），
// 不是给设计者看的口号——它会直接出现在 `/providers` 这类响应里被用户读到。
type Delivery struct {
	// Channel 交付通道（五条之一，或 ChannelNone）。
	Channel string `json:"channel"`
	// Effect 默认生效时刻（immediate/next_run/next_session/next_process）。
	Effect string `json:"effect"`
	// Note 可执行的一句话说明。
	Note string `json:"note"`
}

// Applicability 某条目在**某一个** runtime 上的可达性。
//
// Status 的六个取值各自对应一条不同的用户动作：
//
//	projected      可用（可直接选）
//	written        已物化到该 runtime 的配置文件（T5；新进程启动时读，未重启前不算已生效）
//	untested       会投放/会写，但该 runtime 的投影（或 T5 落点）尚未经实测——不替用户担保底座读得到
//	not_configured 声明了但本进程没解析出落点（去配 MATE_PROJECTION_DIRS / 检查 adapter 声明）
//	not_applicable 底座不支持或未注册（换 runtime，或注册它）
//	disabled       被显式关掉（改回 enabled:true）
//
// 类型定义于 M6 从 `internal/dto/dtolibrary` **原样迁入**：JSON 形状逐字不变（前端零改），
// `dtolibrary` 侧留类型别名，既有调用点零改。
type Applicability struct {
	Runtime string `json:"runtime"`
	OK      bool   `json:"ok"`
	Status  string `json:"status"`
	// Reason 人话原因（**不是错误码**）：调用方要能把这句话直接显示给用户。
	Reason string `json:"reason,omitempty"`
}

// Applicability 的 Status 取值（唯一事实源，测试与前端按它对齐）。
const (
	StatusProjected     = "projected"
	StatusWritten       = "written"
	StatusUntested      = "untested"
	StatusNotConfigured = "not_configured"
	StatusNotApplicable = "not_applicable"
	StatusDisabled      = "disabled"
)

// 交付通道词表（设计「交付矩阵」一节）：五条互不替代的通道。
const (
	// ChannelDialEnv T1：注入 runtime 子进程 env（provider 凭据、权限策略）。
	ChannelDialEnv = "dial_env"
	// ChannelSessionConfig T2：写 ACP 会话的配置项（会话级模型覆盖、默认模型快路径）。
	ChannelSessionConfig = "session_config"
	// ChannelSessionInject T3：建会话时注入会话的**能力集**（MCP server 与其派生 tool）。
	ChannelSessionInject = "session_inject"
	// ChannelMaterialize T4：把能力正文物化到底座读根（skill；每个能力一个目录）。
	ChannelMaterialize = "materialize"
	// ChannelConfigWrite T5：整份写 runtime 的配置文件（平台模型清单；平台独占该文件）。
	ChannelConfigWrite = "config_write"
	// ChannelNone **不是**交付通道：该资源不向底座交付任何东西（automation 是平台自身的调度器）。
	//
	// 显式给这个取值而不是让 Channel 留空：留空与「零值/未声明」不可分辨，守卫与调用方都
	// 会把一条真实的"无通道"误读成缺口。
	ChannelNone = "none"
)

// 交付的生效时刻词表（设计「交付矩阵」一节的右栏）。
const (
	// EffectImmediate 立即（活会话的就地变更；automation 的写面在平台内立即生效）。
	EffectImmediate = "immediate"
	// EffectNextRun 下一个轮次边界（代际迁移发生的地方；有在途轮次或有未应答权限/提问时顺延）。
	EffectNextRun = "next_run"
	// EffectNextSession 下一个新会话（已声明能力的会话在轮次边界迁移）。
	EffectNextSession = "next_session"
	// EffectNextProcess 下一个 runtime 进程（配置与读根都在进程启动时被读）。
	EffectNextProcess = "next_process"
)

// 资源 kind（本表覆盖的种类）。
//
// 与 `internal/persistence/model/enum.go` 的 `DefinitionKind*` 逐一对应（pkg 层不许 import internal，
// 故这里只能重述取值，由 `TestDeliveryMatrixCoversKinds` 断言两边对齐）；`KindTool` 是
// **派生**种类（随所属 MCP server 每轮重建，只在内存），不在 `DefinitionKind*` 里。
const (
	// KindProvider provider 凭据档案（密钥 / 上游 type）。
	KindProvider = "provider"
	// KindModel 平台模型清单（路由 + models[] + 显示名 + 每个 runtime 的默认模型）。
	KindModel = "model"
	// KindSkill skill 定义。
	KindSkill = "skill"
	// KindMcp MCP server。
	KindMcp = "mcp"
	// KindAutomation 自动化定义（平台自身的调度器）。
	KindAutomation = "automation"
	// KindTool 工具（派生数据，随所属 MCP server）。
	KindTool = "tool"
)

// table 交付矩阵本体：**一处声明**，供 DTO/前端/守卫共用。
//
// 每一条都必须有可执行的人话 Note——守卫会断言它非空（空 Note 的声明等于没声明）。
var table = map[string]Delivery{
	KindProvider: {
		Channel: ChannelDialEnv, Effect: EffectNextRun,
		Note: "密钥以 <TYPE>_API_KEY 注入 runtime 子进程 env；改它会让引用该档案的会话在下一轮重建连接" +
			"（有在途轮次或有未应答权限/提问时顺延到再下一轮）",
	},
	KindModel: {
		Channel: ChannelConfigWrite, Effect: EffectNextProcess,
		Note: "整份生成该 runtime 的模型配置文件（平台独占该文件，不合并用户内容），新进程启动时读它；" +
			"改清单会让引用该 runtime 的会话在轮次边界换代际（未实测落点的 runtime 只读不写）",
	},
	KindSkill: {
		Channel: ChannelMaterialize, Effect: EffectNextProcess,
		Note: "把 skill 正文物化到底座读根（每个能力一个目录），由新进程启动时扫描；" +
			"写面末尾重校后，已声明能力的会话在轮次边界迁移",
	},
	KindMcp: {
		Channel: ChannelSessionInject, Effect: EffectNextRun,
		Note: "把 MCP server 注入会话的能力集（启用 ∩ transport ∩ 白名单）；" +
			"新会话立即带上，已声明能力的会话在下一个轮次边界迁移",
	},
	KindAutomation: {
		Channel: ChannelNone, Effect: EffectImmediate,
		Note: "平台自身的调度器：不向任何底座交付，写面立即在平台内生效（下一个调度刻度即可能触发一轮会话）；" +
			"要改底座侧行为请改它引用的凭据",
	},
	KindTool: {
		Channel: ChannelSessionInject, Effect: EffectNextRun,
		Note: "派生数据（随所属 MCP server 每轮重建，只在内存）：没有自己的交付动作，" +
			"server 注入到哪它就到哪——改工具要去改那个 server 的定义",
	},
}

// For 按资源 kind 查这张静态交付表。
//
// 返回 false 表示这个 kind **没有**声明（不是"无通道"：无通道的种类会显式声明 ChannelNone）。
// 调用方必须把 false 当缺口处理，而不是拿零值 Delivery 继续渲染——零值会输出
// `{"channel":"","effect":"","note":""}`，看起来像一条已经声明好的通道。
func For(kind string) (Delivery, bool) {
	d, ok := table[kind]
	return d, ok
}

// Kinds 本表声明过的资源种类（升序）。
//
// 它**不是**「平台拥有的资源种类」的事实源——那是 `internal/persistence/model/enum.go` 的 `DefinitionKind*`
// （pkg 层不许 import internal，故不能在这里直接引用）。这里返回的是本表的覆盖面，
// 由 `TestDeliveryMatrixCoversKinds` 负责断言两者对齐。
func Kinds() []string {
	out := make([]string, 0, len(table))
	for k := range table {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Channels 五条交付通道的词表（不含 ChannelNone）。
func Channels() []string {
	return []string{
		ChannelDialEnv, ChannelSessionConfig, ChannelSessionInject,
		ChannelMaterialize, ChannelConfigWrite,
	}
}

// Effects 生效时刻的词表。
func Effects() []string {
	return []string{EffectImmediate, EffectNextRun, EffectNextSession, EffectNextProcess}
}

// IsDeliveryChannel 是否是一条**交付**通道（ChannelNone 不算：它表示"不向底座交付"）。
func IsDeliveryChannel(channel string) bool {
	return slices.Contains(Channels(), channel)
}

// ConfigApplicability 组装一条 T5（`config_write`）的逐 runtime 可达性结论（M5 模型面用）。
//
// `where` 是落点的人话描述（配置文件路径 + 递入方式）。`written=true` = 清单**已经**物化到
// 该 runtime 的配置文件（status=`written`：文件写了，但配置在**进程启动时**才被读，
// 未重启前不算已生效）；false = 该 runtime 的落点尚未实测，**未实测前只读不写**
// （status=`untested`）。两者都不许互相冒充——把 untested 说成 written 正是本方案要避免的
// 「界面说生效了、底座其实没读」。
func ConfigApplicability(runtime, where string, written bool) Applicability {
	if written {
		return Applicability{Runtime: runtime, OK: true, Status: StatusWritten,
			Reason: where + "（平台独占该文件；runtime 新进程启动时读）"}
	}
	return Applicability{Runtime: runtime, OK: true, Status: StatusUntested,
		Reason: where + "尚未在本仓实测：未实测前只读不写，清单不会递给它"}
}
