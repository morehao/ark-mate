package runtimeconn

import (
	"reflect"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// TestACPSignalTranslation ACP 事件 → 领域信号的**逐字段**翻译（C2 事件载荷那一刀从
// `app/run` 搬来，断言随迁）。
//
// 为什么单独钉：翻译在适配器里、消费在用例层，中间隔着 `DialOptions.OnEvent` 的接口形状。
// 漏译一个字段的表现是"前端某列空了"而不是任何错误；漏译一个 kind 的表现是"事件不推进"。
func TestACPSignalTranslation(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindMessage,
			Payload: acp.MessageDelta{Text: "hi", MessageID: "m1"}})
		want := event.Signal{SessionID: "s1", Kind: event.KindMessage,
			Payload: event.MessageDelta{Text: "hi", MessageID: "m1"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v want %+v", got, want)
		}
	})

	t.Run("thought", func(t *testing.T) {
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindThought,
			Payload: acp.ThoughtDelta{Text: "think", MessageID: "m2"}})
		want := event.Signal{SessionID: "s1", Kind: event.KindThought,
			Payload: event.ThoughtDelta{Text: "think", MessageID: "m2"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v want %+v", got, want)
		}
	})

	t.Run("tool_call_and_update", func(t *testing.T) {
		in := acp.ToolCallInfo{ID: "c1", Title: "edit", Status: "pending", Kind: "edit",
			Input: map[string]any{"path": "a"}, Output: "ok", ContentText: "done"}
		want := event.ToolCall{ID: "c1", Title: "edit", Status: "pending", Kind: "edit",
			Input: map[string]any{"path": "a"}, Output: "ok", ContentText: "done"}
		for _, kind := range []string{acp.KindToolCall, acp.KindToolUpdate} {
			got := acpSignal(acp.Event{SessionID: "s1", Kind: kind, Payload: in})
			if !reflect.DeepEqual(got.Payload, want) {
				t.Fatalf("%s: got %+v want %+v", kind, got.Payload, want)
			}
		}
	})

	t.Run("permission", func(t *testing.T) {
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindPermission,
			Payload: acp.PermissionInfo{SessionID: "s1", Title: "t", Allowed: true,
				Rejected: false, OptionID: "once", Always: true}})
		want := event.Permission{SessionID: "s1", Title: "t", Allowed: true,
			Rejected: false, OptionID: "once", Always: true}
		if !reflect.DeepEqual(got.Payload, want) {
			t.Fatalf("got %+v want %+v", got.Payload, want)
		}
	})

	t.Run("config_options", func(t *testing.T) {
		opts := []acp.ConfigOption{{ID: "mode", Name: "模式"}}
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindConfigOptions,
			Payload: acp.ConfigOptionsInfo{SessionID: "s1", Options: opts}})
		want := event.ConfigOptions{SessionID: "s1", Options: opts}
		if !reflect.DeepEqual(got.Payload, want) {
			t.Fatalf("got %+v want %+v", got.Payload, want)
		}
	})

	t.Run("commands", func(t *testing.T) {
		cmds := []acp.CommandInfo{{Name: "review", Description: "审查", Hint: "路径"}, {Name: "init"}}
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindCommands,
			Payload: acp.CommandsInfo{SessionID: "s1", Commands: cmds}})
		want := event.Commands{SessionID: "s1", Commands: cmds}
		if !reflect.DeepEqual(got.Payload, want) {
			t.Fatalf("got %+v want %+v", got.Payload, want)
		}
	})

	// 没有专门翻译分支的 kind：载荷**原样透传**（由领域自己的 switch 决定发不发）。
	t.Run("usage_passthrough", func(t *testing.T) {
		type usage struct{ Used int }
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindUsage, Payload: usage{Used: 7}})
		if got.Kind != event.KindUsage || !reflect.DeepEqual(got.Payload, usage{Used: 7}) {
			t.Fatalf("got %+v", got)
		}
	})

	// 认不出的 kind：kind 原样透传（领域 switch 没有 default → 不发布，与搬来之前同义）。
	t.Run("unknown_kind", func(t *testing.T) {
		got := acpSignal(acp.Event{SessionID: "s1", Kind: "bogus_kind", Payload: "x"})
		if got.Kind != event.Kind("bogus_kind") || got.Payload != "x" {
			t.Fatalf("got %+v", got)
		}
	})

	// 载荷形状不符：不猜，原样留给领域（领域按自己的类型断言跳过）。
	t.Run("wrong_payload_shape", func(t *testing.T) {
		got := acpSignal(acp.Event{SessionID: "s1", Kind: acp.KindMessage, Payload: 42})
		if got.Payload != 42 {
			t.Fatalf("got %+v", got.Payload)
		}
	})
}
