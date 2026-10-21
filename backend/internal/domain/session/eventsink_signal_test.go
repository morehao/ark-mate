package session

import (
	"testing"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/platform/object/objusage"
	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// 本文件是 C2 第一刀（事件与轮次）的**非 ACP 桩**：整条事件出口只喂 `event.Signal`，
// 文件里不出现任何 `infra/acp` 类型。
//
// 它钉两件事：
//  1. 领域的事件出口真的只认领域语言——把 ACP 事件类型从这里换掉之后，语义逐条不变；
//  2. 翻译后的载荷形状（尤其是权限留痕那份**会被原样落库**的结构）与改造前一致。
//
// 失效方式（为什么值得一条独立用例）：翻译层写错一个字段不会让任何东西变红——事件照常推、
// 界面照常渲染，只是权限卡上的标题/决策或工具调用的结果正文静默丢失，而事件行已经落进库。

// newSignalSinkRig 造一个只带总线、句柄表与工具记忆的运行期状态机。
//
// 其余端口留 nil：它们的语义是"未装配即无副作用"（诊断/目录/取消面），不是这些用例要验的。
func newSignalSinkRig(t *testing.T) (*Runner, *eventbus.Bus) {
	t.Helper()
	pl, bus := testPlane(t, t.TempDir())
	h := NewHandles()
	h.BindProvider("native-1", "conv-1")
	r := NewRunner(RunnerDeps{
		Plane:      pl,
		Handles:    h,
		ToolMemory: NewToolCallMemory(),
	})
	return r, bus
}

// TestEventSinkConsumesDomainSignalsOnly 各 kind 的领域信号 → 平台事件的类型与载荷。
func TestEventSinkConsumesDomainSignalsOnly(t *testing.T) {
	r, bus := newSignalSinkRig(t)
	sink := r.EventSink("rtx")

	sink(event.Signal{Kind: event.KindMessage, SessionID: "native-1",
		Payload: event.MessageDelta{Text: "hi", MessageID: "m1"}})
	sink(event.Signal{Kind: event.KindThought, SessionID: "native-1",
		Payload: event.ThoughtDelta{Text: "想"}})
	sink(event.Signal{Kind: event.KindToolCall, SessionID: "native-1",
		Payload: event.ToolCall{ID: "c1", Title: "edit", Status: "pending", Kind: "edit"}})
	sink(event.Signal{Kind: event.KindToolUpdate, SessionID: "native-1",
		Payload: event.ToolCall{ID: "c1", Status: event.ToolStatusCompleted, ContentText: "ok"}})
	sink(event.Signal{Kind: event.KindUsage, SessionID: "native-1",
		Payload: objusage.UsageInfo{Used: 10, Size: 100}})
	sink(event.Signal{Kind: event.KindPermission, SessionID: "native-1",
		Payload: event.Permission{SessionID: "native-1", Title: "t", Allowed: true, OptionID: "allow"}})
	sink(event.Signal{Kind: event.KindConfigOptions, SessionID: "native-1",
		Payload: event.ConfigOptions{SessionID: "native-1", Options: []string{"opaque"}}})
	sink(event.Signal{Kind: event.KindCommands, SessionID: "native-1",
		Payload: event.Commands{SessionID: "native-1", Commands: []string{"opaque"}}})

	evs := bus.EventsAll()
	types := make([]string, 0, len(evs))
	for _, e := range evs {
		if e.AggregID != "conv-1" {
			t.Fatalf("事件必须落进**平台**会话聚合（原生 id 先解析）：%+v", e)
		}
		// 过程状态（run.status）是**旁路**帧：它按源信号插在业务事件之前出现，
		// 本用例只验业务事件的序列，逐条钉它会变成"实现顺序的快照"。
		if e.Type == BusRunStatus {
			continue
		}
		types = append(types, e.Type)
	}
	want := []string{
		BusMessage, BusThought, BusToolCall, BusToolUpdate, BusUsage,
		BusPermission, BusConfigOptions, BusCommands,
	}
	if len(types) != len(want) {
		t.Fatalf("事件序列不符：got %v want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("type 序列不符：got %v want %v", types, want)
		}
	}

	// 载荷键是**冻结的契约**（`TestToolPayloadKeysAreClosed` 钉白名单）：这里只验翻译没丢字段。
	m, _ := payloadOf(evs, BusMessage).(map[string]any)
	if m["text"] != "hi" || m["msg_id"] != "m1" || m["runtime"] != "rtx" {
		t.Fatalf("message 载荷不符：%+v", payloadOf(evs, BusMessage))
	}
	tc, _ := payloadOf(evs, BusToolCall).(map[string]any)
	if tc["tool_call_id"] != "c1" || tc["title"] != "edit" || tc["kind"] != "edit" {
		t.Fatalf("tool_call 载荷不符：%+v", payloadOf(evs, BusToolCall))
	}
	perm, ok := payloadOf(evs, BusPermission).(event.Permission)
	if !ok || perm.Title != "t" || !perm.Allowed || perm.OptionID != "allow" {
		t.Fatalf("权限留痕必须原样透出（它会被原样落库）：%+v", payloadOf(evs, BusPermission))
	}
}

// TestEventSinkDropsUnknownSignalKinds 认不出的 kind 不发布任何事件（与改造前同义）。
func TestEventSinkDropsUnknownSignalKinds(t *testing.T) {
	r, bus := newSignalSinkRig(t)
	sink := r.EventSink("rtx")
	sink(event.Signal{Kind: event.Kind("bogus"), SessionID: "native-1", Payload: "x"})
	sink(event.Signal{Kind: event.KindMessage, SessionID: "unknown-native",
		Payload: event.MessageDelta{Text: "迟到帧"}})
	if n := len(bus.EventsAll()); n != 0 {
		t.Fatalf("未知 kind / 无映射会话都不该发布事件，实际 %d 条", n)
	}
}

// TestToolMemoryMergesDomainToolCalls 工具记忆的合并语义在域类型上同样成立
// （只覆盖非空字段：status-only 的 update 不得把命令行抹掉）。
func TestToolMemoryMergesDomainToolCalls(t *testing.T) {
	m := NewToolCallMemory()
	m.Remember(event.ToolCall{ID: "c1", Title: "bash", Kind: "execute",
		Input: map[string]any{"command": "ls"}})
	m.Remember(event.ToolCall{ID: "c1", Status: event.ToolStatusCompleted, ContentText: "out"})
	got, ok := m.Get("c1")
	if !ok || got.Title != "bash" || got.Status != event.ToolStatusCompleted || got.ContentText != "out" {
		t.Fatalf("非空字段应合并：%+v", got)
	}
	if _, ok := got.Input.(map[string]any); !ok {
		t.Fatalf("status-only 的更新抹掉了 Input：%#v", got.Input)
	}
}

// payloadOf 取指定 type 的第一帧载荷（没有 → nil）。
func payloadOf(evs []eventbus.Event, typ string) any {
	for _, e := range evs {
		if e.Type == typ {
			return e.Payload
		}
	}
	return nil
}
