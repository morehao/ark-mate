package manager

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// TestRunMaterializationAtRunEnd 验证 P1 runs 物化不变量：
//   - 每轮（用户消息 → 终态事件）生成一条 run：user_text 取首条用户消息，
//     first/last_seq 记录事件窗口，status/stop_reason/error_code 对齐终态；
//   - 轮次窗口从上一 run 的 last_seq 之后起算（多轮不串）；
//   - 无 store 时 ListRuns 返回空（读模型仅 sqlite 模式存在）。
func TestRunMaterializationAtRunEnd(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s-run"})

	// 第一轮：成功。
	u1 := svc.publish(BusUserMessage, "s-run", map[string]any{"text": "第一问"})
	svc.publish(BusMessage, "s-run", map[string]any{"text": "回答一"})
	end1 := svc.publish(BusRunEnded, "s-run", map[string]any{"stop_reason": "end_turn"})

	// 第二轮：失败。
	u2 := svc.publish(BusUserMessage, "s-run", map[string]any{"text": "第二问（会失败）"})
	errEnd := svc.publish(BusRunError, "s-run", map[string]any{"code": "agent_error", "class": "agent"})

	runs, err := st.ListRuns(ctx, "s-run")
	if err != nil || len(runs) != 2 {
		t.Fatalf("应 2 条 run: n=%d err=%v %+v", len(runs), err, runs)
	}
	r0, r1 := runs[0], runs[1]
	if r0.Status != SessionStatusCompleted || r0.UserText != "第一问" || r0.StopReason != "end_turn" {
		t.Fatalf("run0 不符: %+v", r0)
	}
	if r0.FirstSeq != u1.Seq || r0.LastSeq != end1.Seq {
		t.Fatalf("run0 窗口不符: %+v (u1=%d end1=%d)", r0, u1.Seq, end1.Seq)
	}
	if r1.Status != SessionStatusFailed || r1.UserText != "第二问（会失败）" || r1.ErrorCode != "agent_error" {
		t.Fatalf("run1 不符: %+v", r1)
	}
	if r1.FirstSeq != u2.Seq || r1.LastSeq != errEnd.Seq {
		t.Fatalf("run1 窗口不符: %+v (u2=%d err=%d)", r1, u2.Seq, errEnd.Seq)
	}
	if r1.FirstTokenMS != 0 {
		t.Fatalf("run1 无 assistant 输出 first_token_ms 应为 0: %+v", r1)
	}
	if got, _ := st.RunLastSeq(ctx, "s-run"); got != errEnd.Seq {
		t.Fatalf("RunLastSeq=%d want %d", got, errEnd.Seq)
	}

	// Service 读面。
	items, err := svc.ListRuns(ctx, "s-run")
	if err != nil || len(items) != 2 {
		t.Fatalf("ListRuns: n=%d err=%v", len(items), err)
	}
	if items[1].ErrorCode != "agent_error" || items[0].Status != SessionStatusCompleted {
		t.Fatalf("ListRuns 内容不符: %+v", items)
	}

	// 内存模式（无 store）→ 空读模型。
	memSvc := NewService(reg, nil)
	if empty, err := memSvc.ListRuns(ctx, "s-run"); err != nil || len(empty) != 0 {
		t.Fatalf("内存模式 ListRuns 应为空: %+v err=%v", empty, err)
	}
}

// TestRunWindowStartsAtUserMessage 轮界裁剪：一轮从**首条用户消息**起算，窗口起点与本轮输入之间
// 夹着的**会话级**事件（session.created / session.resumed）不属于这一轮。
//
// 不裁掉它们的两个实测后果（2026-10-14 用户报「只问一个问题，messages 里却有三条」）：
//   - messages 物化把 session.created 当成一条 assistant 时间线 → 落出一条正文为空的行；
//   - runID 取裁剪前的首事件 seq，与 first_seq（首条用户消息）错位。
func TestRunWindowStartsAtUserMessage(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s-win"})

	// 会话前置事件（真实路径由 session_start / ensureSession 发出，先于本轮 user_message）。
	svc.publish(BusSessionCreated, "s-win", map[string]any{"session_id": "s-win", "runtime": "stub"})
	svc.publish(BusSessionResumed, "s-win", map[string]any{"session_id": "s-win", "reason": "restart"})
	u := svc.publish(BusUserMessage, "s-win", map[string]any{"text": "你好"})
	svc.publish(BusMessage, "s-win", map[string]any{"msg_id": "m1", "text": "你好呀"})
	end := svc.publish(BusRunEnded, "s-win", map[string]any{"stop_reason": "end_turn"})

	runs, err := st.ListRuns(ctx, "s-win")
	if err != nil || len(runs) != 1 {
		t.Fatalf("应 1 条 run: n=%d err=%v", len(runs), err)
	}
	r := runs[0]
	if r.FirstSeq != u.Seq || r.LastSeq != end.Seq {
		t.Fatalf("run 窗口应为「首条用户消息 → 终态」: %+v (u=%d end=%d)", r, u.Seq, end.Seq)
	}
	if want := fmt.Sprintf("r-s-win-%d", u.Seq); r.ID != want {
		t.Fatalf("runID 应与 first_seq 同源 %q，got %q", want, r.ID)
	}
	// 轮起点 = 首条用户消息的时刻（会话前置那两条不算进本轮时长/首令牌耗时）。
	evs, _, err := st.EventsAfter(ctx, "s-win", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var userEv model.EventEntity
	for _, e := range evs {
		if e.Seq == u.Seq {
			userEv = e
		}
	}
	if userEv.Seq == 0 || !r.StartedAt.Equal(userEv.CreatedAt) {
		t.Fatalf("run 起点应为用户消息时刻 %s，got %s", userEv.CreatedAt, r.StartedAt)
	}
	rows, err := st.ListMessages(ctx, "s-win")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Role != "user" || rows[1].Role != "assistant" {
		t.Fatalf("messages 应只有 user + assistant 两行（无空 assistant 前置行）: %+v", rows)
	}
	if rows[1].Text != "你好呀" {
		t.Fatalf("assistant 行正文不符: %q", rows[1].Text)
	}
}

// TestRunUsageExposedInListRuns runs.usage_json 在 ListRuns 读面被解析为 RunResp.Usage：
// 有上报 → 结构体；空串/无上报 → Usage 为 nil（omitempty）。
func TestRunUsageExposedInListRuns(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s-usage"})

	now := time.Now().UTC()
	seed := func(id, status, usageJSON string, firstTokenMS int64) {
		t.Helper()
		if err := st.CreateRun(ctx, model.RunEntity{
			ID: id, SessionID: "s-usage", Status: status, UsageJSON: usageJSON,
			FirstTokenMS: firstTokenMS,
			StartedAt:    now.Add(-time.Minute), EndedAt: now,
		}); err != nil {
			t.Fatalf("CreateRun(%s): %v", id, err)
		}
	}
	seed("r-1", "completed", `{"used":135,"size":128000}`, 714)
	seed("r-2", "failed", `{"used":150,"size":128000,"cost_amount":0.5,"cost_currency":"USD"}`, 0)
	seed("r-3", "cancelled", ``, 0)

	items, err := svc.ListRuns(ctx, "s-usage")
	if err != nil || len(items) != 3 {
		t.Fatalf("ListRuns: n=%d err=%v", len(items), err)
	}
	if items[0].Usage == nil || items[0].Usage.Used != 135 || items[0].Usage.Size != 128000 {
		t.Fatalf("run1 usage 不符: %+v", items[0].Usage)
	}
	if items[0].FirstTokenMS != 714 {
		t.Fatalf("run1 first_token_ms=%d want 714", items[0].FirstTokenMS)
	}
	if items[1].Usage == nil || items[1].Usage.CostAmount != 0.5 || items[1].Usage.CostCurrency != "USD" {
		t.Fatalf("run2 usage 不符: %+v", items[1].Usage)
	}
	if items[2].Usage != nil {
		t.Fatalf("run3 无上报应为 nil Usage: %+v", items[2].Usage)
	}
}
