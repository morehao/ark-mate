package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

// TestRestoreMarksInProgressAsInterrupted 重启恢复时残留 in_progress 会话收敛为 interrupted
// （可操作态：列表可停止/重发），且不误伤已完成会话。
func TestRestoreMarksInProgressAsInterrupted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forge.db")
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// 第一次进程：登记一个中断中 + 一个已完成会话（写库）。
	s1 := NewService(reg, eventbus.New())
	s1.EnableStore(st)
	s1.registerSession(&SessionRec{ID: "s-int", AgentID: "agent-echo", Runtime: "echo", Status: SessionStatusInProgress})
	s1.registerSession(&SessionRec{ID: "s-done", AgentID: "agent-echo", Runtime: "echo", Status: SessionStatusCompleted})

	// 第二次进程（重启）：恢复后 in_progress → interrupted，completed 原样保留。
	s2 := NewService(reg, eventbus.New())
	s2.EnableStore(st)
	if _, _, err := s2.RestoreFromStore(ctx); err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	if got := s2.sessionRec("s-int"); got == nil || got.Status != SessionStatusInterrupted {
		t.Fatalf("s-int 应收敛为 interrupted, got %+v", got)
	}
	if got := s2.sessionRec("s-done"); got == nil || got.Status != SessionStatusCompleted {
		t.Fatalf("s-done 不应被改动: %+v", got)
	}
	// DB 侧同步更新（再次重启也保持 interrupted）。
	s3 := NewService(reg, eventbus.New())
	s3.EnableStore(st)
	if _, _, err := s3.RestoreFromStore(ctx); err != nil {
		t.Fatalf("二次 RestoreFromStore: %v", err)
	}
	if got := s3.sessionRec("s-int"); got == nil || got.Status != SessionStatusInterrupted {
		t.Fatalf("二次重启仍应为 interrupted: %+v", got)
	}
}
