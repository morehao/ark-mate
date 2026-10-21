package runtimeconn

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// ——— ACP → 领域信号的翻译（C2 事件载荷那一刀从 `app/run` 搬来）———
//
// 会话域的事件出口只认 `domain/session/event` 的类型（`Runner.EventSink`）。ACP 的线上事件在
// **适配器**这一侧翻译：领域与用例层因此都不认识 `acp.Event`/`acp.Kind*`/`acp.ToolCallInfo` 一族。
//
// 翻译的口径只有一条：**逐字段搬运，不改语义**。载荷键、事件 type 与去重键都在领域里，
// 这里动一个字段就可能改掉落库的 JSON 或前端渲染的字段（事件 type 由
// `TestEventTypeContractFrozen` 冻结、工具载荷键由 `TestToolPayloadKeysAreClosed` 双侧钉住）。
//
// 认不出的 kind **原样透传**（`event.Kind(ev.Kind)`）：领域的 switch 没有 default，未知种类
// 不发布任何事件——与搬来之前逐字同义（那时的 switch 也不认）。

// acpSignal 把一条 ACP 事件翻成领域信号。
func acpSignal(ev acp.Event) event.Signal {
	sig := event.Signal{Kind: event.Kind(ev.Kind), SessionID: ev.SessionID, Payload: ev.Payload}
	switch ev.Kind {
	case acp.KindMessage:
		if d, ok := ev.Payload.(acp.MessageDelta); ok {
			sig.Payload = event.MessageDelta{Text: d.Text, MessageID: d.MessageID}
		}
	case acp.KindThought:
		if d, ok := ev.Payload.(acp.ThoughtDelta); ok {
			sig.Payload = event.ThoughtDelta{Text: d.Text, MessageID: d.MessageID}
		}
	case acp.KindToolCall, acp.KindToolUpdate:
		if d, ok := ev.Payload.(acp.ToolCallInfo); ok {
			sig.Payload = acpToolCallSignal(d)
		}
	case acp.KindPermission:
		if d, ok := ev.Payload.(acp.PermissionInfo); ok {
			sig.Payload = event.Permission{
				SessionID: d.SessionID, Title: d.Title, Allowed: d.Allowed,
				Rejected: d.Rejected, OptionID: d.OptionID, Always: d.Always,
			}
		}
	case acp.KindConfigOptions:
		if d, ok := ev.Payload.(acp.ConfigOptionsInfo); ok {
			// `Options` 是不透明载荷（配置项那一刀再换域类型）：领域只把它原样转发进事件。
			sig.Payload = event.ConfigOptions{SessionID: d.SessionID, Options: d.Options}
		}
	case acp.KindCommands:
		if d, ok := ev.Payload.(acp.CommandsInfo); ok {
			sig.Payload = event.Commands{SessionID: d.SessionID, Commands: d.Commands}
		}
	}
	return sig
}

// acpToolCallSignal 翻译一条工具调用详情。
func acpToolCallSignal(d acp.ToolCallInfo) event.ToolCall {
	return event.ToolCall{
		ID: d.ID, Title: d.Title, Status: d.Status, Kind: d.Kind,
		Input: d.Input, Output: d.Output, ContentText: d.ContentText,
	}
}
