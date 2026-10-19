// Package dtosystem 是系统面对外契约（`/meta` 与 `/capabilities`）。
//
// 为什么这些类型不在 `controller/ctrsystem` 里：`/meta` 的载荷清单（`PayloadInfo`）同时被
// 桌面壳的命令行解析、诊断命令（`mate doctor`）与装配根使用；把它留在控制器包会让
// `app`/`cli` 反向 import `controller`（v3.1 §3.2.1 规则 4）。契约归 dto，投影归 service。
package dtosystem

// PayloadInfo 随包运行时载荷的一条（M3-4 起填）。
//
// 值来自**壳的**三级解析结果（设计 §6.5：显式配置 → 随包 → PATH）：只有壳做了那次解析，
// 核心自己不探测本机安装，只把壳交来的清单原样透出。壳的清单里还有 `entry`（包内入口路径），
// 那是壳的内部布局，**刻意不进本结构**——`/meta` 的契约只回答"命中了哪一份、来自哪一级"。
type PayloadInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Source 命中级别：bundled（随包）| runtime-env（显式覆盖）| path（PATH 上找到）。
	// 取值由壳给出并在启动期被核心校验（非法值直接拒绝启动），因为"来源"是诊断面最会骗人的字段。
	Source string `json:"source"`
}

// RuntimeInfo 构建期固定的底座身份（v19）。
//
// 与前端 `packages/types/src/runtime.ts` 的 `RuntimeInfo` **逐字段一致**（两侧手工一致，
// 由界面验收脚本端到端覆盖）。
type RuntimeInfo struct {
	// ID 底座 id（= `GET /runtime/diagnostics` 的 `runtime`，也 = 会话的 `runtime_id`）。
	ID string `json:"id"`
	// Name 底座的展示名（界面标题用它；空 = 取不到，回落用 ID）。
	Name string `json:"name"`
	// Version 底座版本（取不到为空串：核心**不探测**本机安装）。
	Version string `json:"version"`
}

// ProductInfo 产品面事实（`/meta` 的 `product` 段；「关于」面板只消费它）。
//
// **刻意没有执行引擎字段**：`/meta.runtime` 仍然存在（诊断页读它），但产品面不消费它——
// 用户问"这个产品是什么"时，答案不该是"它跑在哪个底座上"。这条边界由前端「关于」面板的
// 文案守卫钉住（不许出现底座名/底座词）。
type ProductInfo struct {
	// Name 产品标识（`ark-mate`）。
	Name string `json:"name"`
	// DisplayName 展示名（界面标题用它；空 = 回落 Name）。
	DisplayName string `json:"displayName"`
	// Version 产品版本（= 核心版本；**不另造第二事实源**）。
	Version string `json:"version"`
	// IdentityVersion 产品身份版本（提示词文案的代数）。它只进 `/meta` 与日志，不进 prompt。
	IdentityVersion string `json:"identityVersion"`
	// Tagline 一句话定位（「关于」面板与产品名同屏出现）。
	Tagline string `json:"tagline"`
}

// MetaInfo `/v1/mate/meta` 的响应（字段契约见计划 §5 M1-7/M2-3）。
type MetaInfo struct {
	CoreVersion string `json:"coreVersion"`
	WebBuildID  string `json:"webBuildId"`
	APIContract string `json:"apiContract"`
	// WebPath 资产目录的**绝对路径**；未接资产层时为 null（**显式 null**，客户端按可空字段处理）。
	WebPath *string `json:"webPath"`
	// WebDigest 逐文件 sha256 的确定性合成；未接资产层时为 null。
	WebDigest *string       `json:"webDigest"`
	Home      string        `json:"home"`
	Payloads  []PayloadInfo `json:"payloads"`
	// Runtime 底座身份（构建期固定）。它**总是出现**（不是可空字段）：装配层一定知道它是谁。
	Runtime RuntimeInfo `json:"runtime"`
	// Product 产品面事实（产品名/版本/身份版本/定位）。**总是出现**，且**不含**执行引擎字段。
	//
	// 与 `Runtime` 并存是刻意的：诊断面要知道"这份部署跑在哪个底座上"，而产品面要知道
	// "用户拿到的是什么产品"。两者的消费者不同，合并成一个段会让"关于"面板顺手读到底座名。
	Product ProductInfo `json:"product"`
}

// Entries 这份部署对外提供哪些入口。
type Entries struct {
	Browser bool `json:"browser"`
	Desktop bool `json:"desktop"`
}

// CapabilityEntry 一条能力的声明。
type CapabilityEntry struct {
	// ID 能力 id（闭集，与前端 @ark-mate/desktop-contract 的 CapabilityId 一一对应）。
	ID string `json:"id"`
	// Available 对**本次请求的入口**是否可用。
	Available bool `json:"available"`
	// BrowserFallback 浏览器入口的等价路径（V3 单向一致性的机器判据）：
	// 任何 available=true 且被某个流程依赖的能力，这里都不得是 none（tray 例外：纯增强、不阻塞流程）。
	BrowserFallback string `json:"browserFallback"`
}

// Capabilities `/v1/mate/capabilities` 的响应。
type Capabilities struct {
	Entries      Entries           `json:"entries"`
	Capabilities []CapabilityEntry `json:"capabilities"`
}
