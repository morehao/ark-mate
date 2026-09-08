package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

// TestRunMaterializationAtTurnEnd 验证 P1 runs 物化不变量：
//   - 每轮（用户消息 → 终态事件）生成一条 run：user_text 取首条用户消息，
//     first/last_seq 记录事件窗口，status/stop_reason/error_code 对齐终态；
//   - 轮次窗口从上一 run 的 last_seq 之后起算（多轮不串）；
//   - 无 store 时 ListRuns 返回空（读模型仅 sqlite 模式存在）。
func TestRunMaterializationAtTurnEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forge.db")
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s-run", AgentID: "agent-echo", Runtime: "echo"})

	// 第一轮：成功。
	u1 := svc.publish(BusUserMessage, "s-run", map[string]any{"text": "第一问"})
	svc.publish(BusMessage, "s-run", map[string]any{"text": "回答一"})
	end1 := svc.publish(BusTurnEnded, "s-run", map[string]any{"stop_reason": "end_turn"})

	// 第二轮：失败。
	u2 := svc.publish(BusUserMessage, "s-run", map[string]any{"text": "第二问（会失败）"})
	errEnd := svc.publish(BusTurnError, "s-run", map[string]any{"code": "agent_error", "class": "agent"})

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
