package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session"
	"testing"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
)

// TestRestoreMarksInProgressAsInterrupted 重启恢复时残留 in_progress 会话收敛为 interrupted
// （可操作态：列表可停止/重发），且不误伤已完成会话。
func TestRestoreMarksInProgressAsInterrupted(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()

	// 第一次进程：登记一个中断中 + 一个已完成会话（写库）。
	s1 := NewService(reg, eventbus.New())
	s1.EnableStore(st)
	s1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-int", Status: session.SessionStatusInProgress})
	s1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-done", Status: session.SessionStatusCompleted})

	// 第二次进程（重启）：恢复后 in_progress → interrupted，completed 原样保留。
	s2 := NewService(reg, eventbus.New())
	s2.EnableStore(st)
	if _, _, err := s2.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	if got := s2.sessionPlane.Rec("s-int"); got == nil || got.Status != session.SessionStatusInterrupted {
		t.Fatalf("s-int 应收敛为 interrupted, got %+v", got)
	}
	if got := s2.sessionPlane.Rec("s-done"); got == nil || got.Status != session.SessionStatusCompleted {
		t.Fatalf("s-done 不应被改动: %+v", got)
	}
	// DB 侧同步更新（再次重启也保持 interrupted）。
	s3 := NewService(reg, eventbus.New())
	s3.EnableStore(st)
	if _, _, err := s3.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("二次 RestoreFromStore: %v", err)
	}
	if got := s3.sessionPlane.Rec("s-int"); got == nil || got.Status != session.SessionStatusInterrupted {
		t.Fatalf("二次重启仍应为 interrupted: %+v", got)
	}
}
