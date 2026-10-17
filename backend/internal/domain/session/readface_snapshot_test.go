package session

import (
	"fmt"
	"sync"
	"testing"
)

// TestSessionSnapshotIsOneMoment 语义：一次取锁拿到的 `resp` 与 `run` 属于**同一时刻**，
// 且不存在 → ok=false（不返回空壳）。
//
// 行为那半（并发写 map 时不再裸读）只在 `-race` 下可见：`make test-race` 会跑本文件；
// 无 `-race` 时这一段仍验证语义（快照能看到最新的 title / run 投影）。
func TestSessionSnapshotIsOneMoment(t *testing.T) {
	pl := NewPlane()
	pl.RegisterSession(&SessionRec{ID: "ses_a", Status: SessionStatusInProgress, Title: "A"})
	pl.recordRunStatus("ses_a", 1, map[string]any{
		"run_id": "run_1", "status": RunStatusPreparing,
	})

	resp, run, ok := pl.SessionSnapshot("ses_a")
	if !ok {
		t.Fatal("已登记的会话快照应 ok=true")
	}
	if resp.ID != "ses_a" || resp.Title != "A" {
		t.Fatalf("快照 resp 不符: %+v", resp)
	}
	if run == nil || run.Status != RunStatusPreparing || run.Seq != 1 {
		t.Fatalf("快照 run 不符: %+v（want preparing / seq=1）", run)
	}

	// 更新之后必须看到新值（同一把锁下，不存在"resp 是旧的、run 是新的"）。
	pl.Mutate("ses_a", func(r *SessionRec) { r.Title = "B" })
	pl.recordRunStatus("ses_a", 2, map[string]any{
		"run_id": "run_1", "status": RunStatusModelStreaming,
	})
	resp, run, ok = pl.SessionSnapshot("ses_a")
	if !ok || resp.Title != "B" || run == nil || run.Status != RunStatusModelStreaming || run.Seq != 2 {
		t.Fatalf("更新后的快照不符: resp=%+v run=%+v ok=%v", resp, run, ok)
	}

	if _, run, ok := pl.SessionSnapshot("ses_missing"); ok || run != nil {
		t.Fatal("不存在的会话必须 ok=false 且 run=nil")
	}
}

// TestSessionSnapshotRacesWithRegistration 并发写/读：注册新会话（写 map）与快照（读 map）交错。
//
// 这是 `-race` 下的判据：修之前 `SessionResp` 导出且裸读 `pl.sessions[id]`，而 `svcsession`
// 的调用点不持锁——race detector 会直接报在那一行。修之后读面自己取锁，本用例只证明
// "并发下不再有裸读"，不证明性能。
func TestSessionSnapshotRacesWithRegistration(t *testing.T) {
	pl := NewPlane()
	pl.RegisterSession(&SessionRec{ID: "ses_a", Status: SessionStatusInProgress, Title: "A"})
	pl.recordRunStatus("ses_a", 1, map[string]any{"status": RunStatusPreparing})

	var wg sync.WaitGroup
	done := make(chan struct{})

	// 写侧：不断注册新会话（map 写入）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 200; i++ {
			pl.RegisterSession(&SessionRec{ID: fmt.Sprintf("ses_w%d", i), Status: SessionStatusInProgress})
		}
	}()

	// 读侧：反复快照同一条会话（map 读取）。
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_, _, _ = pl.SessionSnapshot("ses_a")
					_, _, _ = pl.SessionSnapshot("ses_missing")
				}
			}
		}()
	}
	wg.Wait()

	if _, _, ok := pl.SessionSnapshot("ses_a"); !ok {
		t.Fatal("并发读写之后 ses_a 仍在")
	}
}
