// Package identity 产品身份的**纯值对象**与交付模式。
//
// 它是 leaf 包：不 import `internal/`，也不认识任何底座（`pkg/` 内也不依赖别的包）。
// 三条边界由本包承担：
//
//	Spec  —— 一次交付的身份事实（两个面：系统提示 personaPrefix 与首轮 user-context 块）；
//	Mode  —— `MATE_IDENTITY_MODE` 的取值域与"两个面各要不要用"的判据；
//	Fingerprint —— 幂等判据（同一份身份重复交付必须收敛）。
//
// **为什么系统提示与首轮块分成两个字段而不是一个**：它们的作用面不同（每轮 vs 每个执行上下文
// 一次），失败面也不同（patch 写坏会让底座拒启，首轮块只会让 prompt 变长）。合并成一个字段
// 就没法表达 `system` 模式（只走系统提示、失败即无身份）与 `user` 模式（只走首轮块）。
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Spec 一次产品身份交付的全部事实。
//
// 字段来源分两类，别混着用：
//   - `Name` / `DisplayName` / `Version` / `Tagline` 是**产品面**（`/meta.product` 与「关于」面板）；
//   - `SystemPrompt` / `UserContext` 是**给模型的两面**（前者每轮重复，后者每个执行上下文一次）。
//
// 身份文本由构建期常量生成：不插值会话内容、不插值用户输入、不含版本号（版本只进 `/meta` 与
// 日志）。原因见 `Fingerprint` 的注释——身份文本一漂，系统提示的 KV 缓存前缀就跟着失效。
type Spec struct {
	// Version 产品身份版本（`v1`、`v2`…）。它只回答"这段文案是哪一代"。
	Version string
	// Name 产品名（英文标识，`ark-mate`）。
	Name string
	// DisplayName 展示名（界面与 `/meta` 用它；与 Name 分开是因为将来可能有大小写/间隔差异）。
	DisplayName string
	// Tagline 一句话定位（「关于」面板与 `/meta.product.tagline`）。
	Tagline string
	// SystemPrompt 系统提示里的产品身份段（每轮生效；空 = 不使用该面）。
	//
	// 它可能被写成某个底座的模板（dsh 的 `personaPrefix` 是 Nunjucks 模板），所以**不得含
	// `{{` / `}}`**——未知变量在首轮组装时才抛错（不是启动时），那会让每次会话都失败。
	SystemPrompt string
	// UserContext 首轮注入的 `<product_identity>` 块（每个执行上下文一次；空 = 不使用该面）。
	UserContext string
}

// Enabled 两个面是否至少有一个有内容。都为空 = 该部署不声明产品身份（`off` 模式）。
func (s Spec) Enabled() bool {
	return strings.TrimSpace(s.SystemPrompt) != "" || strings.TrimSpace(s.UserContext) != ""
}

// UsesSystemPrompt 是否交付系统提示面。
func (s Spec) UsesSystemPrompt() bool { return strings.TrimSpace(s.SystemPrompt) != "" }

// UsesUserContext 是否交付首轮块面。
func (s Spec) UsesUserContext() bool { return strings.TrimSpace(s.UserContext) != "" }

// Fingerprint 幂等判据：由**文案与版本**算出，不由产物算出。
//
// 为什么由文案算（与 `pkg/resource` 的同步指纹同一条讲究）：同一份身份重复交付必须收敛
// （不重写字节、不重装参数）。若用产物（含时间戳/格式差异）算，任何一次无意义的格式化都会
// 让"身份变了"看起来成立，而真正变了的文案反而被吞掉。
//
// 版本**并进**指纹（产品面事实），但**不进 prompt 正文**：版本进正文会让系统提示前缀随发布
// 漂移，KV 缓存失效——这是两件事，别混。
func (s Spec) Fingerprint() string {
	h := sha256.New()
	for _, part := range []string{
		"identity/v1", s.Version, s.Name, s.DisplayName, s.Tagline, s.SystemPrompt, s.UserContext,
	} {
		fmt.Fprintf(h, "%s\x00", part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Mode 产品身份的交付模式（`MATE_IDENTITY_MODE` 的取值域）。
//
// 四个取值不是"开关的四种写法"，而是四条**语义不同**的路径（见 Mode.Apply）：
//
//	auto   —— 发布默认：系统提示为主、首轮块为兜底（两个面都交付）；
//	system —— 只走系统提示：失败即无身份，**不静默回退**（用于验证主路径）；
//	user   —— 只走首轮块：启动时还要**主动撤回**已有的身份 patch；
//	off    —— 都不走：启动时同样主动撤回，产品面（`/meta`、关于）仍然保留。
type Mode string

const (
	// ModeAuto 发布默认（系统提示 + 首轮兜底）。
	ModeAuto Mode = "auto"
	// ModeSystem 只走系统提示。
	ModeSystem Mode = "system"
	// ModeUser 只走首轮块。
	ModeUser Mode = "user"
	// ModeOff 不交付身份（撤回既有 patch）。
	ModeOff Mode = "off"
)

// Modes 合法取值（稳定顺序；错误文案与配置校验共用）。
var Modes = []Mode{ModeAuto, ModeSystem, ModeUser, ModeOff}

// ValidModes 取值集合（写面/配置校验共用；与 Modes 同源，避免两份清单漂移）。
var ValidModes = func() map[Mode]bool {
	out := make(map[Mode]bool, len(Modes))
	for _, m := range Modes {
		out[m] = true
	}
	return out
}()

// ModeList 取值的中文枚举（错误文案用）。
func ModeList() string {
	parts := make([]string, 0, len(Modes))
	for _, m := range Modes {
		parts = append(parts, string(m))
	}
	return strings.Join(parts, "|")
}

// ParseMode 解析 `MATE_IDENTITY_MODE`。
//
// **空值 = auto**（未设该变量与显式写 auto 逐字同义）；非法取值返回错误而**不静默回落**——
// 回落的代价是"用户以为关掉了身份、实际还在交付"，而两个形态在界面上长得一模一样。
func ParseMode(raw string) (Mode, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ModeAuto, nil
	}
	m := Mode(v)
	if !ValidModes[m] {
		return "", fmt.Errorf("身份模式需为 %s 之一，得到 %q", ModeList(), raw)
	}
	return m, nil
}

// Apply 把模式作用到一份身份上，返回**本模式该交付的两个面**。
//
// 三条映射是刻意的：
//   - `auto` 两个面都给（首轮块是冗余的防御层：patch 交付失败、远程 runtime、老会话恢复时仍有身份）；
//   - `system` 只给系统提示——失败就真的没有身份（用于验证主路径；回退会让 A1 的判据失去意义）；
//   - `user` / `off` 把系统提示清空 ⇒ 交付路径据此**撤回**已有的 patch（只"不写"是不够的：
//     上一次的 patch 文件还在、`--patch` 还装着，底座照旧读旧提示）。
func (m Mode) Apply(s Spec) Spec {
	switch m {
	case ModeSystem:
		s.UserContext = ""
	case ModeUser:
		s.SystemPrompt = ""
	case ModeOff:
		s.SystemPrompt, s.UserContext = "", ""
	}
	return s
}

// Describe 一句给人看的话（启动日志/诊断用；不含底座名）。
func (m Mode) Describe() string {
	switch m {
	case ModeSystem:
		return "只走系统提示（失败即无身份，不回退首轮块）"
	case ModeUser:
		return "只走首轮块（已撤回系统提示面）"
	case ModeOff:
		return "不交付身份（产品面仍保留）"
	default:
		return "系统提示为主、首轮块兜底"
	}
}

// HasTemplateBraces 文本里是否出现了模板插值标记（`{{` / `}}`）。
//
// 判据必须能在**启动期**回答：某底座的 persona 是模板，未知变量在**首轮组装**时才抛错——
// 那时用户已经点了发送，界面只会显示一条与真实原因无关的失败。所以身份文案在落盘前必须
// 用这条判据静态拦掉，而不是等底座在运行时告诉我们。
func HasTemplateBraces(text string) bool {
	return strings.Contains(text, "{{") || strings.Contains(text, "}}")
}
