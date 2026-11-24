package session

import "testing"

// TestTouchLastActiveAdvances 证明活跃时间接线：注册后 TouchLastActive 把 LastActiveAt 推进为
// 非零的「现在」（毫秒）；未知会话 no-op 不 panic。
func TestTouchLastActiveAdvances(t *testing.T) {
	pl := NewPlane()
	pl.RegisterSession(&SessionRec{ID: "s1"})
	rec := pl.Rec("s1")
	if rec == nil {
		t.Fatal("注册后应能取回会话")
	}
	if rec.LastActiveAt != 0 {
		t.Fatalf("新建时 LastActiveAt 应为 0（未记录），got %d", rec.LastActiveAt)
	}
	pl.TouchLastActive("s1")
	if got := pl.Rec("s1").LastActiveAt; got <= 0 {
		t.Fatalf("TouchLastActive 后 LastActiveAt 应为正的 Unix 毫秒，got %d", got)
	}
	// 未知会话：no-op，不 panic、不影响别的会话。
	pl.TouchLastActive("does-not-exist")
}
