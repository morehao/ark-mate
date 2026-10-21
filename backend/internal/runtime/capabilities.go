package runtime

import "github.com/morehao/ark-mate/internal/adapters/outbound/acp"

// ——— 连接期能力事实的平台值（C2：内核契约去协议化）———
//
// 这些事实此前是 `acp.Capabilities`（含一个 `Raw acpsdk.AgentCapabilities` 字段——SDK 类型）。
// 现在它们是**平台值**：内核与会话域只认显式布尔，适配器若需要原始 SDK 结构，自己在内部保留
// （协议细节不进内圈）。
//
// 为什么去掉 `Raw`：内圈读它只为了两个问题——"能不能 load/resume"（已展开成
// `LoadSession` / `SessionResume`，取值与 SDK 那两处判定逐字对应）与"鉴权方式长什么样"
// （已展开成 `AuthGuidance`）。保留一个 SDK 结构等于把协议永久钉进平台契约，"换一个非 ACP 底座"
// 就得改内核——正是这次收口要拆掉的那条边。

// CapabilityFacts 一条连接握手到的事实（工具 / MCP / 会话持久化 / 权限面）。
//
// 与 `Capabilities`（`Spec.Caps` 的**静态基线声明**）刻意分开：那个是"按源码声明、保守取值"的
// 建库事实，这个是"这条连接握手时实际说了什么"。把两者合成一个类型会让"静态基线被当成事实"
// 重新变得容易（ADR-14 的教训）。
type CapabilityFacts struct {
	// SessionsPersistent 支持 load/list/resume/fork 等持久会话能力。
	SessionsPersistent bool
	// MultiSessionConcurrent 同连接可同时存在多个会话。
	MultiSessionConcurrent bool
	// Images prompt 可携带图片。
	Images bool
	// EmbeddedContext prompt 可内嵌 Resource 上下文。
	EmbeddedContext bool
	// AuthMethods agent 声明的鉴权方式标签（非空则需走 Authenticate 流程）。
	AuthMethods []string
	// AuthGuidance 鉴权方式的展示信息（空 = agent 未声明鉴权要求）。
	AuthGuidance []AuthGuidance

	// —— 会话生命周期能力：上层据此决定「能不能 load/resume/list/close/fork」——
	//
	// 关键区别（历史实测）：底座可以只广告 load 而不广告 resume，两者都**不**等于 delete。
	LoadSession   bool
	SessionClose  bool
	SessionList   bool
	SessionResume bool
	SessionDelete bool
	SessionFork   bool
}

// AuthGuidance 一条鉴权方式的展示信息（给用户一条确切的补救路径）。
type AuthGuidance struct {
	// MethodID 传给 authenticate 的 methodId。
	MethodID string
	// Name 人话名称。
	Name string
	// Description agent 给出的说明（可能是一句英文原文）。
	Description string
	// Terminal 是否需要在终端交互完成登录。
	Terminal bool
	// Command 可执行命令行（为空且 Terminal=true 时由调用方用启动命令补齐）。
	Command []string
}

// CapabilityFactsOfACP 把协议能力事实折成平台值（**适配器边界的唯一转换入口**）。
//
// 公开给适配器：`adapters/outbound/runtimeconn` 拨号后手里只有 `*acp.Client`，它必须借这一处转换。
func CapabilityFactsOfACP(c acp.Capabilities) CapabilityFacts {
	out := CapabilityFacts{
		SessionsPersistent:     c.SessionsPersistent,
		MultiSessionConcurrent: c.MultiSessionConcurrent,
		Images:                 c.Images,
		EmbeddedContext:        c.EmbeddedContext,
		LoadSession:            c.LoadSession,
		SessionClose:           c.SessionClose,
		SessionList:            c.SessionList,
		SessionResume:          c.SessionResume,
		SessionDelete:          c.SessionDelete,
		SessionFork:            c.SessionFork,
	}
	if len(c.AuthMethods) > 0 {
		out.AuthMethods = append([]string(nil), c.AuthMethods...)
	}
	if len(c.AuthGuidance) > 0 {
		out.AuthGuidance = make([]AuthGuidance, 0, len(c.AuthGuidance))
		for _, g := range c.AuthGuidance {
			out.AuthGuidance = append(out.AuthGuidance, AuthGuidance{
				MethodID: g.MethodID, Name: g.Name, Description: g.Description,
				Terminal: g.Terminal, Command: append([]string(nil), g.Command...),
			})
		}
	}
	return out
}
