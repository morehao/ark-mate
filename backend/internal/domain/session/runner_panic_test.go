package session

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/core/protocol"
)

// panickyACP 的 Prompt 直接 panic，用来验证 executeRun 的 panic 兜底收尾。
type panickyACP struct{}

func (panickyACP) Prompt(context.Context, []protocol.PromptPart) (string, objusage.TokenUsage, error) {
	panic("boom from PromptACP")
}
func (panickyACP) Cancel(context.Context) error           { return nil }
func (panickyACP) ConfigOptions() []protocol.ConfigOption { return nil }

// stubBusyTracker 记录 Unmark，验证兜底路径确实释放了 busy。
type stubBusyTracker struct{ unmarked bool }

func (s *stubBusyTracker) Mark(protocol.Conn, string)   {}
func (s *stubBusyTracker) Unmark(protocol.Conn, string) { s.unmarked = true }
func (s *stubBusyTracker) Clear(protocol.Conn)          {}
func (s *stubBusyTracker) Len(protocol.Conn) int        { return 0 }

// TestExecuteRunRecoversPanicAndFinalizes 回归：run goroutine 内 panic 必须被兜底收尾。
//
// 修复前 executeRun 无 recover：panic 会 terminate 整个 daemon；即便加了 recover 却漏收尾，
// as.busy 会永久为 true、t.done 永不关闭——该会话此后每个 prompt 都返回 run_in_progress（卡死）。
// 本用例注入一个 Prompt 必 panic 的会话，断言：不 panic 逃逸、busy 释放、run 清空、done 关闭、
// 会话被标 failed、发出 run.error。
func TestExecuteRunRecoversPanicAndFinalizes(t *testing.T) {
	plane := NewPlane()
	plane.RegisterSession(&SessionRec{ID: "s1", Status: SessionStatusInProgress})
	busy := &stubBusyTracker{}
	r := NewRunner(RunnerDeps{Plane: plane, Busy: busy})

	as := NewActiveSession(HandleParams{ID: "s1", Runtime: "rt", Session: panickyACP{}})
	run, _, err := as.BeginRun(context.Background(), "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if !as.Busy() {
		t.Fatal("BeginRun 后应忙")
	}

	// 直接调（同步）——panic 必须被 executeRun 内部的 defer recover 兜住，不逃逸到这里。
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("panic 逃逸到调用方：executeRun 的 recover 没生效（rec=%v）", rec)
			}
		}()
		r.executeRun(as, run, nil)
	}()

	// 收尾三件：busy 释放、run 清空、done 关闭。
	if as.Busy() {
		t.Fatal("panic 兜底后会话仍 busy（会永久 run_in_progress）")
	}
	if as.RunID() != "" {
		t.Fatalf("panic 兜底后仍有在途 run: %q", as.RunID())
	}
	if !busy.unmarked {
		t.Fatal("panic 兜底未调用 BusyTracker.Unmark")
	}
	select {
	case <-run.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("panic 兜底未关闭 run.Done()（waitwatch 会挂满超时）")
	}

	// 会话状态应为 failed（如实收口，不是静默）。
	var st string
	for _, rec := range plane.Snapshot() {
		if rec.ID == "s1" {
			st = rec.Status
		}
	}
	if st != SessionStatusFailed {
		t.Fatalf("panic 兜底后会话状态应为 failed，got %q", st)
	}
}
