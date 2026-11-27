package session

import (
	"testing"
	"time"
)

// TestTieringSnapshot 只读分层快照：按 last_active_at 判层、按 id 升序、不动状态。
func TestTieringSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	day := 24 * time.Hour
	pl := NewPlane()
	// 三条会话：刚活跃 / 10 天前 / 40 天前；加一条从未活跃（0）。
	pl.RegisterSession(&SessionRec{ID: "s-b", LastActiveAt: now.UnixMilli()})
	pl.RegisterSession(&SessionRec{ID: "s-a", LastActiveAt: now.Add(-10 * day).UnixMilli()})
	pl.RegisterSession(&SessionRec{ID: "s-c", LastActiveAt: now.Add(-40 * day).UnixMilli()})
	pl.RegisterSession(&SessionRec{ID: "s-d", LastActiveAt: 0})

	snap := pl.TieringSnapshot(now, DefaultTieringPolicy())
	if len(snap) != 4 {
		t.Fatalf("应有 4 条快照，got %d", len(snap))
	}
	// 按 id 升序：s-a(warm) s-b(hot) s-c(purge) s-d(hot)
	want := map[string]Tier{"s-a": TierWarm, "s-b": TierHot, "s-c": TierPurge, "s-d": TierHot}
	if snap[0].SessionID != "s-a" || snap[1].SessionID != "s-b" || snap[2].SessionID != "s-c" || snap[3].SessionID != "s-d" {
		t.Fatalf("快照应按 sessionID 升序，got %v", snap)
	}
	for _, s := range snap {
		if s.Tier != want[s.SessionID] {
			t.Errorf("%s tier=%s want %s", s.SessionID, s.Tier, want[s.SessionID])
		}
	}
	// 只读：快照不改会话记录。
	if r := pl.Rec("s-c"); r.LastActiveAt != now.Add(-40*day).UnixMilli() {
		t.Fatalf("快照不应改动记录，s-c.LastActiveAt 变了：%d", r.LastActiveAt)
	}
}
