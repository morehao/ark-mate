package sessions_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/morehao/ark-mate/internal/control/sessions"
)

// TestRegistry_RevokeClosesAllForSID 登记两条同 sid 连接 + 一条他 sid，撤销该 sid 应只关这两条。
func TestRegistry_RevokeClosesAllForSID(t *testing.T) {
	r := sessions.NewRegistry()
	var a, b, other int32
	r.Register("sid-1", func() { atomic.AddInt32(&a, 1) })
	r.Register("sid-1", func() { atomic.AddInt32(&b, 1) })
	r.Register("sid-2", func() { atomic.AddInt32(&other, 1) })

	if got := r.ActiveCount("sid-1"); got != 2 {
		t.Fatalf("sid-1 应有 2 条连接，得到 %d", got)
	}

	n, err := r.RevokeBySID(context.Background(), "sid-1")
	if err != nil {
		t.Fatalf("撤销出错: %v", err)
	}
	if n != 2 {
		t.Fatalf("应撤销 2 条，得到 %d", n)
	}
	if a != 1 || b != 1 {
		t.Fatalf("sid-1 的两条都应被关闭：a=%d b=%d", a, b)
	}
	if other != 0 {
		t.Fatalf("sid-2 不应受影响，other=%d", other)
	}
	if got := r.ActiveCount("sid-1"); got != 0 {
		t.Fatalf("撤销后 sid-1 应为空，得到 %d", got)
	}
	if got := r.ActiveCount("sid-2"); got != 1 {
		t.Fatalf("sid-2 应保留 1 条，得到 %d", got)
	}
}

// TestRegistry_UnregisterIdempotent 注销幂等，且注销后撤销不再触发。
func TestRegistry_UnregisterIdempotent(t *testing.T) {
	r := sessions.NewRegistry()
	var called int32
	unregister := r.Register("sid-x", func() { atomic.AddInt32(&called, 1) })
	unregister()
	unregister() // 幂等
	if got := r.ActiveCount("sid-x"); got != 0 {
		t.Fatalf("注销后应为 0，得到 %d", got)
	}
	if _, _ = r.RevokeBySID(context.Background(), "sid-x"); called != 0 {
		t.Fatalf("注销后撤销不应再触发，called=%d", called)
	}
}

// TestRegistry_EmptySIDNotRegistered 空 sid 不登记（避免无 sid 连接聚到空键被误撤）。
func TestRegistry_EmptySIDNotRegistered(t *testing.T) {
	r := sessions.NewRegistry()
	var called int32
	unregister := r.Register("", func() { atomic.AddInt32(&called, 1) })
	if got := r.ActiveCount(""); got != 0 {
		t.Fatalf("空 sid 不应登记，得到 %d", got)
	}
	if n, _ := r.RevokeBySID(context.Background(), ""); n != 0 || called != 0 {
		t.Fatalf("撤销空 sid 应为空操作：n=%d called=%d", n, called)
	}
	unregister() // 不 panic
}

// TestRegistry_RevokeTwiceSecondIsNoop 再次撤销同一 sid 不重复触发（登记表已清空）。
func TestRegistry_RevokeTwiceSecondIsNoop(t *testing.T) {
	r := sessions.NewRegistry()
	var called int32
	r.Register("sid-y", func() { atomic.AddInt32(&called, 1) })
	_, _ = r.RevokeBySID(context.Background(), "sid-y")
	n2, _ := r.RevokeBySID(context.Background(), "sid-y")
	if called != 1 || n2 != 0 {
		t.Fatalf("二次撤销应为空操作：called=%d n2=%d", called, n2)
	}
}
