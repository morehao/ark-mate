package session

import (
	"testing"

	"github.com/morehao/ark-mate/internal/core/eventbus"
)

// 本文件钉 `run.status` 的**域内语义**（P0-1/P0-2）：
//
//	① 唯一铸造点的载荷形状（可选键**缺省即不出现**，不是空串）；
//	② 取值校验（活体 10 态才算，`interrupted` 只许恢复路径铸）；
//	③ 同状态去重（连发的 `model_streaming` 只留一帧，但换工具要留）；
//	④ 「等待是插曲」——进入等待前的状态被记下，决策后 `ResumeRunStatus` 送回去。
//
// ④ 的失效方式是**静默**的：不发这一条，事件流里那一轮就永远停在 `waiting_for_permission`，
// 而卡片已经消掉、那一轮其实已经在跑——界面上没有任何东西会报错，只是头栏一直说「等人」。

func newRunStatusPlane() *Plane {
	// 带一条总线：没有 bus 也没有 store 时事件没有位次（Seq = -1），投影不会更新——
	// 那是"什么都没有"的退化态，不是这些用例要验的东西。
	pl := NewPlane()
	pl.SetBus(eventbus.New())
	return pl
}

// TestRunStatusPayloadShapeUnchangedKeysAreAbsent 载荷形状：可选键空则**不出现**。
func TestRunStatusPayloadShapeUnchangedKeysAreAbsent(t *testing.T) {
	m := RunStatusPayload("ses_1", "run_1", RunStatusPreparing, RunStatusOpts{})
	if m["session_id"] != "ses_1" || m["run_id"] != "run_1" || m["status"] != RunStatusPreparing {
		t.Fatalf("必填键不全：%+v", m)
	}
	if _, ok := m["at"]; !ok {
		t.Fatalf("`at` 必须总是有（零值取 now）：%+v", m)
	}
	for _, k := range []string{"phase", "detail", "tool_call_id", "tool_name", "waiting_for", "retry", "reason"} {
		if _, ok := m[k]; ok {
			t.Fatalf("可选键 %q 在空值时不该出现（空串会让前端以为「有一件事」，而它没有数据）：%+v", k, m)
		}
	}
	full := RunStatusPayload("ses_1", "run_1", RunStatusToolExecuting, RunStatusOpts{
		Phase: "calling_model", Detail: "正在执行 Bash", ToolCallID: "c1", ToolName: "Bash",
		WaitingFor: "tool", Retry: map[string]any{"attempt": 1},
	})
	for _, k := range []string{"phase", "detail", "tool_call_id", "tool_name", "waiting_for", "retry"} {
		if _, ok := full[k]; !ok {
			t.Fatalf("有值时可选键 %q 必须出现：%+v", k, full)
		}
	}
}

// TestRunStatusMintRejectsNonLiveValues 只有 10 个活体取值能经 `PublishRunStatus` 发出。
func TestRunStatusMintRejectsNonLiveValues(t *testing.T) {
	pl := newRunStatusPlane()
	for _, bad := range []string{"", "bogus", RunStatusInterrupted} {
		if evt := pl.PublishRunStatus("ses_1", "run_1", bad, RunStatusOpts{}); evt.Seq != -1 {
			t.Fatalf("取值 %q 不该被铸成事件（返回 Seq=%d）：`interrupted` 只许恢复路径铸，"+
				"实时路径发它等于伪造终态", bad, evt.Seq)
		}
	}
	if _, ok := pl.RunSnapshot("ses_1"); ok {
		t.Fatal("被拒的取值不该留下投影")
	}
	// 恢复路径那一支可以铸（它在 10 态之外，所以必须走独立入口）。
	if evt := pl.PublishRunInterrupted("ses_1", "run_1"); evt.Seq < 0 {
		t.Fatal("恢复收口必须能铸出事件")
	}
	if snap, ok := pl.RunSnapshot("ses_1"); !ok || snap.Status() != RunStatusInterrupted {
		t.Fatalf("恢复收口后投影应为 interrupted：%+v ok=%v", snap, ok)
	}
}

// TestRunStatusDedupesSameStateButNotNewTool 同状态连发只留一帧；换工具要留。
func TestRunStatusDedupesSameStateButNotNewTool(t *testing.T) {
	pl := newRunStatusPlane()
	first := pl.PublishRunStatus("ses_1", "run_1", RunStatusModelStreaming, RunStatusOpts{})
	if first.Seq < 0 {
		t.Fatal("第一次必须发出去")
	}
	again := pl.PublishRunStatus("ses_1", "run_1", RunStatusModelStreaming, RunStatusOpts{})
	if again.Seq != -1 {
		t.Fatalf("同状态连发应被去重（Seq=%d）：一次长工具循环能推几十帧", again.Seq)
	}
	// 换工具：`tool_executing` 的 key 含 tool_call_id，必须各留一帧。
	a := pl.PublishRunStatus("ses_1", "run_1", RunStatusToolExecuting, RunStatusOpts{ToolCallID: "c1", ToolName: "Bash"})
	b := pl.PublishRunStatus("ses_1", "run_1", RunStatusToolExecuting, RunStatusOpts{ToolCallID: "c2", ToolName: "Read"})
	if a.Seq < 0 || b.Seq < 0 {
		t.Fatalf("两个不同工具的状态都必须发出去（a=%d b=%d）：否则界面会一直显示上一个工具", a.Seq, b.Seq)
	}
}

// TestWaitingIsAnInterludeAndResumes 等待是插曲：记下进入前状态，决策后送回去。
func TestWaitingIsAnInterludeAndResumes(t *testing.T) {
	pl := newRunStatusPlane()
	pl.PublishRunStatus("ses_1", "run_1", RunStatusToolExecuting, RunStatusOpts{ToolCallID: "c1", ToolName: "Bash"})
	pl.PublishRunStatus("ses_1", "run_1", RunStatusWaitingForPermission, RunStatusOpts{
		ToolCallID: "c1", ToolName: "Bash", WaitingFor: "permission",
	})
	if snap, ok := pl.RunSnapshot("ses_1"); !ok || snap.Status() != RunStatusWaitingForPermission {
		t.Fatalf("进入等待后投影应为 waiting_for_permission：%+v", snap)
	}

	evt := pl.ResumeRunStatus("ses_1")
	if evt.Seq < 0 {
		t.Fatal("决策落地后必须把进入等待前的状态送回去——否则头栏永久停在「等待你确认授权」")
	}
	snap, ok := pl.RunSnapshot("ses_1")
	if !ok || snap.Status() != RunStatusToolExecuting {
		t.Fatalf("退出等待后投影应回到 tool_executing：%+v ok=%v", snap, ok)
	}
	if snap.Str("tool_name") != "Bash" || snap.Str("tool_call_id") != "c1" {
		t.Fatalf("恢复的状态必须带上原来的工具（否则头栏会丢掉「正在执行 Bash」）：%+v", snap.Payload)
	}

	// hold 已消费：再调一次是 no-op（没有等待就没有「退出等待」）。
	if again := pl.ResumeRunStatus("ses_1"); again.Seq != -1 {
		t.Fatalf("没有 hold 时 ResumeRunStatus 必须是 no-op（got Seq=%d）", again.Seq)
	}
	// 且不该凭空补一条状态（那等于替运行时宣布阶段）。
	if snap, _ := pl.RunSnapshot("ses_1"); snap.Status() != RunStatusToolExecuting {
		t.Fatalf("no-op 不该改投影：%+v", snap)
	}
}

// TestRunStatusProjectionPrefersLaterSeq 投影按位次单调（回放与实时交错时不被旧事件覆盖）。
func TestRunStatusProjectionPrefersLaterSeq(t *testing.T) {
	pl := newRunStatusPlane()
	pl.recordRunStatus("ses_1", 10, RunStatusPayload("ses_1", "run_1", RunStatusToolExecuting, RunStatusOpts{}))
	pl.recordRunStatus("ses_1", 5, RunStatusPayload("ses_1", "run_1", RunStatusPreparing, RunStatusOpts{}))
	if snap, _ := pl.RunSnapshot("ses_1"); snap.Status() != RunStatusToolExecuting {
		t.Fatalf("更早位次的事件不得覆盖更新的投影：%+v", snap)
	}
	if snap, _ := pl.RunSnapshot("ses_1"); snap.Seq != 10 {
		t.Fatalf("投影位次应为 10：%d", snap.Seq)
	}
}

// TestRunStatusInterruptedPayloadCarriesReason 恢复收口的载荷说得出「为什么」且不说「失败」。
func TestRunStatusInterruptedPayloadCarriesReason(t *testing.T) {
	m := RunInterruptedPayload("ses_1", "run_1", "")
	if m["status"] != RunStatusInterrupted {
		t.Fatalf("status 应为 interrupted：%+v", m)
	}
	if m["reason"] != "restart" {
		t.Fatalf("缺省 reason 应为 restart（界面据此说「上一轮因进程重启中断，可重发」）：%+v", m)
	}
	if _, ok := m["error"]; ok {
		t.Fatalf("恢复收口不该带 error：说「失败」是代底座下结论：%+v", m)
	}
	// at 必须是 Unix 毫秒（读面按毫秒透出）。
	if at, ok := m["at"].(int64); !ok || at <= 0 {
		t.Fatalf("at 不是 Unix 毫秒：%#v", m["at"])
	}
}
