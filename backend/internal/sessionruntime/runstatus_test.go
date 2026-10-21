package sessionruntime

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
)

// 本文件钉 **S1 `run.status` 的发射面**（P0-2）。
//
// 为什么需要它：`run.status` 的**取值集与铸造点**由 `domain/session` 的守卫钉住，但那只能证明
// 「如果发了，发得对」。这一组要钉的是**该发的地方发了、不该发的地方没发**：
//
//	① 一轮的开头有 `preparing`、请求发出后有 `model_requesting`；
//	② 三个终态**之前**各有一条收口 `idle`（界面「停止」的判据必须是一个状态，不是"一段时间没事件"）；
//	③ 所有过程状态的 `run_id` 与开轮那一支**同源**（否则界面认不出"这条状态属于哪一轮"）；
//	④ 工具的 `tool_executing` 带 `tool_name`（Q6 选①：头栏那句「正在执行 Bash」）；
//	⑤ `intentional`（停机拆除）一支**零收口**（ADR-8 的前半）——它有源码判据（守卫 9），
//	   但没有行为判据的话，"把 closer 提到 switch 之前"这种改动照样能让源码判据绿（那一支的
//	   分支体确实不含 BusRun，可 closer 在分支之外）。
//
// 另：`model_streaming` 的连发由域里的去重键吞掉，这里顺带钉一次（两条增量只有一帧状态）。

// TestRunStatusSequenceOnRun 一轮问答的过程状态：preparing → model_requesting → idle → run.ended。
func TestRunStatusSequenceOnRun(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "rtx", Session: &stubSession{stop: "end_turn"}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 开轮 + 用户消息 + 3 条过程状态 + 终态 = 6 条。
	evts := feedAll(bus, "s1", 6)
	if len(evts) < 6 {
		t.Fatalf("事件不足（want 6）：%d 条 %+v", len(evts), evts)
	}
	if evts[0].Type != session.BusRunStarted {
		t.Fatalf("第一条应是 run.started，got %q", evts[0].Type)
	}
	runID, _ := evts[0].Payload.(map[string]any)["run_id"].(string)
	if runID == "" {
		t.Fatalf("run.started 载荷缺 run_id")
	}

	// 收齐本轮全部过程状态（按 seq 升序）。
	var statuses []string
	for _, e := range evts {
		if e.Type != session.BusRunStatus {
			continue
		}
		m, _ := e.Payload.(map[string]any)
		if got, _ := m["run_id"].(string); got != runID {
			t.Fatalf("过程状态的 run_id = %q，与开轮那一支 %q 不同源：界面据此认不出「这条状态属于哪一轮」", got, runID)
		}
		s, _ := m["status"].(string)
		if s == "" {
			t.Fatalf("过程状态缺 status：%+v", m)
		}
		statuses = append(statuses, s)
	}
	want := []string{session.RunStatusPreparing, session.RunStatusModelRequesting, session.RunStatusIdle}
	if len(statuses) < len(want) {
		t.Fatalf("过程状态只有 %d 条 %v（want ≥ %d）", len(statuses), statuses, len(want))
	}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("过程状态序列不符：got %v（want 开头 %v）", statuses, want)
		}
	}
	// 收口必须在终态**之前**：否则界面会先看到「结束了」再看到「空闲」。
	if last := evts[len(evts)-1]; last.Type != session.BusRunEnded {
		t.Fatalf("最后一条应是 run.ended，got %q（事件：%+v）", last.Type, evts)
	}
}

// TestRunStatusFromEventSink 工具的 `tool_executing` 带工具名；`model_streaming` 连发只有一帧。
func TestRunStatusFromEventSink(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	// P3 双 id：底层事件只带原生 session id，得先有映射（与 TestEventSinkMapping 同一套夹具）。
	orch.handles.BindProvider("native-1", "s1")
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "rtx"})
	run, _, err := as.BeginRun(context.Background())
	if err != nil {
		t.Fatalf("开轮: %v", err)
	}
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})
	sink := orch.runner.EventSink("rtx")

	// 两条文本增量：只应产生**一帧** `model_streaming`（同状态去重）。
	sink(acp.Event{SessionID: "native-1", Kind: acp.KindMessage, Payload: acp.MessageDelta{Text: "a"}})
	sink(acp.Event{SessionID: "native-1", Kind: acp.KindMessage, Payload: acp.MessageDelta{Text: "b"}})
	// 一次工具调用：`tool_executing` 带 name/id，且在工具事件**之前**。
	sink(acp.Event{SessionID: "native-1", Kind: acp.KindToolCall,
		Payload: acp.ToolCallInfo{ID: "call-1", Title: "Bash", Status: "pending"}})

	evts := feedAll(bus, "s1", 4) // run.status(model_streaming) + 合并后的 message + run.status(tool_executing) + tool_call
	var statuses []map[string]any
	var order []string
	for _, e := range evts {
		order = append(order, e.Type)
		if e.Type == session.BusRunStatus {
			m, _ := e.Payload.(map[string]any)
			statuses = append(statuses, m)
		}
	}
	if len(statuses) != 2 {
		t.Fatalf("过程状态应恰好 2 条（model_streaming 去重后 + tool_executing），got %d：%+v", len(statuses), statuses)
	}
	if statuses[0]["status"] != session.RunStatusModelStreaming {
		t.Fatalf("第一条应是 model_streaming，got %v", statuses[0]["status"])
	}
	if statuses[1]["status"] != session.RunStatusToolExecuting {
		t.Fatalf("第二条应是 tool_executing，got %v", statuses[1]["status"])
	}
	if statuses[1]["tool_name"] != "Bash" || statuses[1]["tool_call_id"] != "call-1" {
		t.Fatalf("tool_executing 必须带 tool_name/tool_call_id（头栏「正在执行 Bash」靠它）：%+v", statuses[1])
	}
	if statuses[0]["run_id"] != run.ID() || statuses[1]["run_id"] != run.ID() {
		t.Fatalf("过程状态的 run_id 必须指向当前那一轮：%+v / %+v", statuses[0], statuses[1])
	}
	// 先有「我要跑 X」再有「X 的结果」。
	idxToolStatus := -1
	idxToolCall := -1
	for i, typ := range order {
		if typ == session.BusRunStatus && idxToolStatus < 0 && i > 0 {
			idxToolStatus = i
		}
		if typ == session.BusToolCall && idxToolCall < 0 {
			idxToolCall = i
		}
	}
	if idxToolStatus < 0 || idxToolCall < 0 || idxToolStatus > idxToolCall {
		t.Fatalf("tool_executing 必须排在 session.tool_call 之前：%v", order)
	}
}

// TestIntentionalRunEmitsNoCloser 停机拆除（intentional）一支**零收口**（ADR-8 的前半）。
func TestIntentionalRunEmitsNoCloser(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: &stubSession{cancelOnCtx: true}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 主动拆除（daemon 停机 / 显式停 agent）：收尾归 interrupted，不按失败/用户取消处理。
	orch.runner.Disconnect(as, true)

	// 收集 2s 内该聚合的全部事件（`feedAll` 到点即返回已有的）。
	evts := feedAll(bus, "s1", 64)
	for _, e := range evts {
		switch e.Type {
		case session.BusRunEnded, session.BusRunError, session.BusRunCancelled:
			t.Fatalf("intentional 一支**不得**发终态 %q：伪终态会污染历史、失败轮列表与重试入口（ADR-8 前半）", e.Type)
		case session.BusRunStatus:
			m, _ := e.Payload.(map[string]any)
			if m["status"] == session.RunStatusIdle {
				t.Fatalf("intentional 一支**不得**发收口 `idle`：收口状态同样是「这一轮到此为止」的宣告，" +
					"在这一支上它与伪终态同罪；崩溃残轮的收口由**恢复路径**补（ADR-8 后半）")
			}
		}
	}
	rec, ok := svc.sessionPlane.Lookup("s1")
	if !ok || rec.Status != session.SessionStatusInterrupted {
		t.Fatalf("会话状态应 interrupted：%+v", rec)
	}
}
