package session

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// 这两条钉的是 `runRowFor` 的**兜底路径**——终态事件载荷没有 `run_id` 时，按"该会话唯一的
// 进行中行"物化本轮。v30 之前它是一条**死代码**（`RunningRows` 过滤 `running`，而写入方写
// `in_progress`），所以没有任何用例覆盖到；修完查询之后，这里必须真的能走通，也必须仍然
// "不唯一就不猜"。
func TestRunRowFallbackMaterializesWithoutRunID(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("跑迁移: %v", err)
	}
	sessionID := objid.New(objid.Session)
	runID := objid.New(objid.Run)
	if err := st.UpsertSession(ctx, model.SessionEntity{
		ID: sessionID, Cwd: t.TempDir(), Status: SessionStatusInProgress,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写会话行: %v", err)
	}
	if _, err := st.AppendEvent(ctx, objid.New(objid.Event), sessionID, BusRunStarted, map[string]any{
		"session_id": sessionID, "run_id": runID, "text": "跑一下",
	}); err != nil {
		t.Fatalf("写 run.started: %v", err)
	}
	if _, err := st.AppendEvent(ctx, objid.New(objid.Event), sessionID, BusUserMessage, map[string]any{
		"text": "跑一下",
	}); err != nil {
		t.Fatalf("写 user_message: %v", err)
	}
	if err := st.CreateRun(ctx, model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: "跑一下",
		Status: SessionStatusInProgress, FirstSeq: 0, LastSeq: -1,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写开轮行: %v", err)
	}

	pl := NewPlane()
	pl.SetStore(st)
	// **载荷刻意不带 run_id**：这正是兜底路径存在的理由（M2 之前的终态事件没有它）。
	pl.MaterializeRun(sessionID, BusRunEnded, map[string]any{"stop_reason": "end_turn"}, 1)

	runs, err := st.ListRuns(ctx, sessionID)
	if err != nil {
		t.Fatalf("读 runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs 行数 = %d（want 1）", len(runs))
	}
	if runs[0].Status != SessionStatusCompleted {
		t.Fatalf("兜底路径没物化：status = %q（want %q）——`RunningRows` 再次恒空就会这样",
			runs[0].Status, SessionStatusCompleted)
	}
	if runs[0].LastSeq != 1 {
		t.Fatalf("窗口右端 = %d（want 1）：终态位次必须写回行", runs[0].LastSeq)
	}
	if runs[0].StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q（want end_turn）", runs[0].StopReason)
	}
}

// TestRunRowFallbackSkipsAmbiguous 保住既有语义：多于一条进行中的行 = 崩溃残轮，
// 兜底路径**不猜**（把终态记在"最近一条"上会静默地把一轮的窗口写到另一轮身上）。
func TestRunRowFallbackSkipsAmbiguous(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("跑迁移: %v", err)
	}
	sessionID := objid.New(objid.Session)
	if err := st.UpsertSession(ctx, model.SessionEntity{
		ID: sessionID, Cwd: t.TempDir(), Status: SessionStatusInProgress,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写会话行: %v", err)
	}
	for i, id := range []string{objid.New(objid.Run), objid.New(objid.Run)} {
		if err := st.CreateRun(ctx, model.RunEntity{
			ID: id, SessionID: sessionID, Status: SessionStatusInProgress,
			FirstSeq: int64(i * 2), LastSeq: -1, StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("写开轮行 %d: %v", i, err)
		}
	}

	pl := NewPlane()
	pl.SetStore(st)
	pl.MaterializeRun(sessionID, BusRunEnded, map[string]any{"stop_reason": "end_turn"}, 2)

	runs, err := st.ListRuns(ctx, sessionID)
	if err != nil {
		t.Fatalf("读 runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs 行数 = %d（want 2）", len(runs))
	}
	for _, r := range runs {
		if r.Status != SessionStatusInProgress || r.LastSeq != -1 {
			t.Fatalf("不唯一的进行中行被物化了：%+v（want 全部保持 in_progress / -1）", r)
		}
	}
}
