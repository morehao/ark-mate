// Package dtolibrary 定义能力 Library 的统一读模型（M2-4）。
//
// Library **不拥有任何数据**（选型 E1）：它是一次请求内的聚合视图，事实源分别是
// `pkg/capability` 的注册表、`manager.agents` 与（后续）`manager.automation`。
package dtolibrary

import "github.com/morehao/ark-mate/internal/object/objdelivery"

// LibraryItem 一条统一能力条目（skill / mcp / agent / tool；command 见 M5）。
//
// 这是面板与输入卡的**唯一读模型**：同一个条目在面板里的样子与在输入卡 `+` 里的样子来自同一份
// 结构，避免"面板显示可用、输入卡里选不到"这类两套读模型漂移。
type LibraryItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // skill | mcp | tool
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
	// 诊断路径不合并）——"对这个底座可用、对那个不可用"是有用信息，压成一个 bool 就没了。
	Applicability []Applicability `json:"applicability"`
	// Warnings 装载期/体检得到的非致命提醒（正文扫描告警、密钥引用解析不出、退役键残留…）。
	Warnings []string `json:"warnings,omitempty"`
}

// Applicability 某条目在**某一个** runtime 上的可达性。
//
// M6 起类型定义迁到 `pkg/object/objdelivery`（交付矩阵与可达性是两个面共用的值对象：
// `/library` 与 `/providers` 用同一套形状），这里留**类型别名**——JSON 形状逐字不变（前端零改），
// 既有调用点也零改。取值语义（projected/written/untested/not_configured/not_applicable/disabled）
// 与注释见 objdelivery.Applicability。
type Applicability = objdelivery.Applicability

// Applicability 的 Status 取值（唯一事实源是 objdelivery，这里只是转发以便既有调用点零改）。
const (
	StatusProjected     = objdelivery.StatusProjected
	StatusWritten       = objdelivery.StatusWritten
	StatusUntested      = objdelivery.StatusUntested
	StatusNotConfigured = objdelivery.StatusNotConfigured
	StatusNotApplicable = objdelivery.StatusNotApplicable
	StatusDisabled      = objdelivery.StatusDisabled
)

// Kind 取值（M5 之前不含 command）。
const (
	KindSkill = "skill"
	KindMcp   = "mcp"
	KindTool  = "tool"
)

// ValidKinds 合法 kind 集合（`?kind=` 校验用；未知值报 invalid_param 而不是静默返回空表——
// 静默会让"我筛了但没有"和"我拼错了"看起来一样）。
var ValidKinds = map[string]bool{
	KindSkill: true, KindMcp: true, KindTool: true,
}

// Origin 取值。
const (
	OriginLocal    = "local"
	OriginImported = "imported"
)
