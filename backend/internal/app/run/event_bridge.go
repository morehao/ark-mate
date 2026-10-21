package run

import (
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— ACP → 领域信号的**翻译边界**（C2 第一刀：事件与轮次）———
//
// 会话域的事件出口只认 `domain/session/event` 的类型（`Runner.EventSink`）。ACP 的线上事件
// 在**适配器**这一侧翻译：领域因此不认识 `acp.Event`/`acp.Kind*`/`acp.ToolCallInfo` 一族。
//
// 翻译的口径只有一条：**逐字段搬运，不改语义**。载荷键、事件 type 与去重键都在领域里，
// 这里动一个字段就可能改掉落库的 JSON 或前端渲染的字段（事件 type 由
// `TestEventTypeContractFrozen` 冻结、工具载荷键由 `TestToolPayloadKeysAreClosed` 双侧钉住）。
//
// 认不出的 kind **原样透传**（`event.Kind(ev.Kind)`）：领域的 switch 没有 default，未知种类
// 不发布任何事件——与改造前逐字同义（那时的 switch 也不认）。

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

// acpEventSink 造一个**ACP 形状**的事件出口：先按需刷新配置项目录缓存，再交给领域出口。
//
// 目录刷新原先在领域的事件出口里做（那时它直接吃 `acp.ConfigOptionsInfo`）。`Options` 换成
// 不透明载荷之后，刷新留在**适配器**：那是"底座推送 → 平台目录"的翻译副作用，不是领域规则。
func (o *Orchestrator) acpEventSink(runtimeID string) func(acp.Event) {
	sink := o.runner.EventSink(runtimeID)
	catalog := runnerCatalog{svc: o.svc}
	return func(ev acp.Event) {
		if ev.Kind == acp.KindConfigOptions {
			if d, ok := ev.Payload.(acp.ConfigOptionsInfo); ok && d.SessionID != "" {
				catalog.PutConfigOptions(runtimeID, runtime.ConfigOptionsOfACP(d.Options))
			}
		}
		sink(acpSignal(ev))
	}
}
