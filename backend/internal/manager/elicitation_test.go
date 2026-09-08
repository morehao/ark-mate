package manager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// TestElicitationResolveAccept 注册 → 用户提交（accept + content）→ 阻塞方拿到决策。
func TestElicitationResolveAccept(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	published := make(chan ElicitationItem, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out acl.ElicitationOutcome
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		out = m.RegisterAndWait(ctx, "s1", acl.ElicitationRequest{
			SessionID: "s1",
			Mode:      "form",
			Message:   "请选择一个环境",
			Schema: &acl.ElicitationSchema{
				Title:      "部署目标",
				Properties: map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}}},
				Required:   []string{"env"},
			},
		}, func(it ElicitationItem) { published <- it })
	}()

	it := <-published
	if it.Mode != "form" || it.Schema == nil {
		t.Fatalf("publish 载荷不符: %+v", it)
	}
	if !strings.HasPrefix(it.ID, "el-") {
		t.Fatalf("id 前缀不符: %s", it.ID)
	}
	if !m.ResolveAccept(it.ID, map[string]any{"env": "prod"}) {
		t.Fatal("ResolveAccept 应命中挂起项")
	}
	wg.Wait()
	if out.Action != "accept" || out.Content["env"] != "prod" {
		t.Fatalf("决策不符: %+v", out)
	}
	if m.pendingCount(it.SessionID) != 0 {
		t.Fatal("决策后挂起表应清空")
	}
}

// TestElicitationTimeoutAutoCancel 无用户决策 → 超时自动按取消处理。
func TestElicitationTimeoutAutoCancel(t *testing.T) {
	m := NewElicitationManager(40 * time.Millisecond)
	start := time.Now()
	out := m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
		SessionID: "s1", Mode: "url", Message: "请打开链接", URL: "https://example.com",
	}, nil)
	if out.Action != "cancel" {
		t.Fatalf("超时应按取消处理: %+v", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("超时未按时收敛")
	}
}

// TestElicitationResolveUnknown 未知 id / 非法 action 分支。
func TestElicitationResolveUnknown(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	if m.ResolveAccept("el-nope", nil) {
		t.Fatal("未知 id 不应命中")
	}
	if m.ResolveDecline("el-nope") || m.ResolveCancel("el-nope") {
		t.Fatal("未知 id 不应命中")
	}
}

// TestElicitationCancelSession session/cancel 把挂起提问按取消回馈并清空挂起表。
func TestElicitationCancelSession(t *testing.T) {
	m := NewElicitationManager(30 * time.Second)
	registered := make(chan string, 1)
	done := make(chan acl.ElicitationOutcome, 1)
	go func() {
		out := m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
			SessionID: "s1", Mode: "form", Message: "q", Schema: &acl.ElicitationSchema{Properties: map[string]any{}},
		}, func(it ElicitationItem) { registered <- it.ID })
		done <- out
	}()
	<-registered // 注册完成（发布事件）后再取消会话
	m.CancelSession("s1")
	out := <-done
	if out.Action != "cancel" {
		t.Fatalf("取消会话后应按取消回馈: %+v", out)
	}
	if n := m.pendingCount("s1"); n != 0 {
		t.Fatalf("取消后挂起表应清空, got %d", n)
	}
}

// pendingCount 供单测断言挂起表收敛。
func (m *ElicitationManager) pendingCount(sessionID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			n++
		}
	}
	return n
}
