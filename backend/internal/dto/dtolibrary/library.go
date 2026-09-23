// Package dtolibrary 定义能力 Library 的统一读模型（M2-4）。
//
// Library **不拥有任何数据**（选型 E1）：它是一次请求内的聚合视图，事实源分别是
// `pkg/capability` 的注册表、`manager.agents` 与（后续）`manager.automation`。
package dtolibrary

// LibraryItem 一条统一能力条目（skill / mcp / agent / tool；command 见 M5）。
//
// 这是面板与输入卡的**唯一读模型**：同一个条目在面板里的样子与在输入卡 `+` 里的样子来自同一份
// 结构，避免"面板显示可用、输入卡里选不到"这类两套读模型漂移。
type LibraryItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // skill | mcp | agent | tool
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Origin 血缘：`local`（本机的定义文件——用户自己写的）/ `imported`（随平台来的或运行时探测出来的：
	// 种子 agent、builtin/observed 工具、MCP 探测出的工具、代码注入的条目）。
	// 判据只有一条且可验证：**有没有事实源文件**（`source_file` 非空 = local）。
	Origin string `json:"origin"`
	// Scope 作用域（global|workspace）；tool 取归属 MCP server 的作用域。
	Scope string `json:"scope"`
	// Enabled 是否生效：skill/mcp = 定义里的 enabled（未声明 = true）；agent/tool = true
	// （平台没有"禁用某个 agent / 某条工具"的开关，硬造一个只会让人以为它有用）。
	Enabled bool `json:"enabled"`
	// SourceFile 事实源文件（代码注入/运行时探测的条目为空）。
	SourceFile string `json:"source_file,omitempty"`
	// Applicability **逐候选 runtime** 的可达性。刻意不给"合并结论"（对标 openwork：
	// 诊断路径不合并）——"对 opencode 可用、对 dsh 不可用"是有用信息，压成一个 bool 就没了。
	Applicability []Applicability `json:"applicability"`
	// Warnings 装载期/体检得到的非致命提醒（正文扫描告警、密钥引用解析不出、退役键残留…）。
	Warnings []string `json:"warnings,omitempty"`
}

// Applicability 某条目在**某一个** runtime 上的可达性。
//
// Status 的五个取值各自对应一条不同的用户动作：
//
//	projected      可用（可直接选）
//	untested       会投放，但该 runtime 的投影声明尚未经实测——不替用户担保底座开箱读到它
//	not_configured 声明了但本进程没解析出落点（去配 FORGE_PROJECTION_DIRS / 检查 adapter 声明）
//	not_applicable 底座不支持或未注册（换 runtime，或注册它）
//	disabled       被显式关掉（改回 enabled:true）
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
	StatusUntested      = "untested"
	StatusNotConfigured = "not_configured"
	StatusNotApplicable = "not_applicable"
	StatusDisabled      = "disabled"
)

// Kind 取值（M5 之前不含 command）。
const (
	KindSkill = "skill"
	KindMcp   = "mcp"
	KindAgent = "agent"
	KindTool  = "tool"
)

// ValidKinds 合法 kind 集合（`?kind=` 校验用；未知值报 invalid_param 而不是静默返回空表——
// 静默会让"我筛了但没有"和"我拼错了"看起来一样）。
var ValidKinds = map[string]bool{
	KindSkill: true, KindMcp: true, KindAgent: true, KindTool: true,
}

// Origin 取值。
const (
	OriginLocal    = "local"
	OriginImported = "imported"
)
