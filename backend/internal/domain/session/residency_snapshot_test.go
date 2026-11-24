package session

import (
	"context"
	"testing"
	"time"
)

// TestResidencySnapshot 只读驻留快照：executing 绝不驱逐、idle 按 LRU 逐、按 id 升序、不动句柄。
func TestResidencySnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	plane := NewPlane()
	handles := NewHandles()

	// a: idle、最久未活跃（最该被逐）；b: idle、较新；c: executing（即便最久也不逐）。
	mk := func(id string, lastMin int64) *ActiveSession {
		plane.RegisterSession(&SessionRec{ID: id, LastActiveAt: now.Add(-time.Duration(lastMin) * time.Minute).UnixMilli()})
		as := NewActiveSession(HandleParams{ID: id, Runtime: "r"})
		handles.Put(as)
		return as
	}
	mk("a", 100)
	mk("b", 1)
	c := mk("c", 999)
	if _, _, err := c.BeginRun(context.Background(), ""); err != nil {
		t.Fatalf("c 开轮失败: %v", err)
	}

	r := NewRunner(RunnerDeps{Plane: plane, Handles: handles})
	// 驻留 3、上限 1 → 名义逐 2；executing 的 c 排除后 idle=[a(100m),b(1m)]，逐最久 2 条 = a 和 b。
	snap := r.ResidencySnapshot(now, ResidencyPolicy{NResident: 1})
	if len(snap) != 3 {
		t.Fatalf("应 3 条快照，got %d", len(snap))
	}
	byID := map[string]ResidencyDecision{}
	for _, d := range snap {
		byID[d.SessionID] = d
	}
	if byID["c"].Evict || !byID["c"].Executing {
		t.Fatalf("executing 的 c 绝不可被逐：%+v", byID["c"])
	}
	if !byID["a"].Evict || !byID["b"].Evict {
		t.Fatalf("idle 的 a、b 应被逐（上限1、驻留3）：a=%v b=%v", byID["a"].Evict, byID["b"].Evict)
	}
	// 升序
	if snap[0].SessionID != "a" || snap[1].SessionID != "b" || snap[2].SessionID != "c" {
		t.Fatalf("应按 id 升序，got %v", snap)
	}
}

// TestResidencySnapshotNeverEvictsExecuting 只有 executing + 上限 0(idle 不足)时，一条都不逐。
func TestResidencySnapshotNeverEvictsExecuting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	plane := NewPlane()
	handles := NewHandles()
	as := NewActiveSession(HandleParams{ID: "x", Runtime: "r"})
	plane.RegisterSession(&SessionRec{ID: "x", LastActiveAt: now.UnixMilli()})
	handles.Put(as)
	if _, _, err := as.BeginRun(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(RunnerDeps{Plane: plane, Handles: handles})
	snap := r.ResidencySnapshot(now, ResidencyPolicy{NResident: 0}) // 0 = 不限制
	for _, d := range snap {
		if d.Evict {
			t.Fatalf("N_resident=0 不限制，不应驱逐任何会话：%+v", d)
		}
	}
}
