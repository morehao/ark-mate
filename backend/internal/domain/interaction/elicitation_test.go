package interaction

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/infra/acp"
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
	if found, mismatch := m.ResolveAccept("s1", it.ID, map[string]any{"env": "prod"}); !found || mismatch {
		t.Fatalf("ResolveAccept 应命中挂起项: found=%v mismatch=%v", found, mismatch)
	}
	wg.Wait()
	if out.Action != "accept" || out.Content["env"] != "prod" {
		t.Fatalf("决策不符: %+v", out)
	}
	if m.PendingCount(it.SessionID) != 0 {
		t.Fatal("决策后挂起表应清空")
	}
}

// TestElicitationCompleteByURLID verify agent 的 elicitation/complete 收尾（URL 模式）：
// 按 ACP 侧 elicitationId 命中挂起项 → 以 accept + reason=complete 收敛（不再挂到超时）；
// 归属不符/未知 id/非 URL 模式/重放一律无副作用。
func TestElicitationCompleteByURLID(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	resolved := make(chan [2]string, 2)
	m.SetOnResolved(func(_, _, action, reason string) {
		resolved <- [2]string{action, reason}
	})

	published := make(chan ElicitationItem, 1)
	var out acl.ElicitationOutcome
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		out = m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
			SessionID: "s1", Mode: "url", Message: "请打开链接", URL: "https://example.com",
			URLID: "acp-el-1",
		}, func(it ElicitationItem) { published <- it })
	}()
	<-published

	if m.CompleteByURLID("other", "acp-el-1") {
		t.Fatal("归属不符的 complete 应被拒")
	}
	if m.CompleteByURLID("s1", "no-such") {
		t.Fatal("未知 elicitationId 应返回 false")
	}
	if !m.CompleteByURLID("s1", "acp-el-1") {
		t.Fatal("命中 URL 模式挂起项应返回 true")
	}
	wg.Wait()
	if out.Action != "accept" {
		t.Fatalf("complete 应按 accept 收敛: %+v", out)
	}
	got := <-resolved
	if got[0] != "accept" || got[1] != "complete" {
		t.Fatalf("收敛事件应为 accept/complete: %+v", got)
	}
	if m.CompleteByURLID("s1", "acp-el-1") {
		t.Fatal("重放的 complete 不应命中（已决）")
	}

	// form 模式即便带同名字段也不收敛（只认 URL 模式）。
	publishedForm := make(chan ElicitationItem, 1)
	var formOut acl.ElicitationOutcome
	var fwg sync.WaitGroup
	fwg.Add(1)
	go func() {
		defer fwg.Done()
		formOut = m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
			SessionID: "s1", Mode: "form", Message: "请填表", URLID: "acp-el-2",
		}, func(it ElicitationItem) { publishedForm <- it })
	}()
	formIt := <-publishedForm
	if m.CompleteByURLID("s1", "acp-el-2") {
		t.Fatal("form 模式不应被 complete 收敛")
	}
	m.ResolveCancel("s1", formIt.ID)
	fwg.Wait()
	if formOut.Action != "cancel" {
		t.Fatalf("form 模式应仍由用户决策收敛: %+v", formOut)
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
	if found, _ := m.ResolveAccept("s1", "el-nope", nil); found {
		t.Fatal("未知 id 不应命中")
	}
	if found, _ := m.ResolveDecline("s1", "el-nope"); found {
		t.Fatal("未知 id 不应命中")
	}
	if found, _ := m.ResolveCancel("s1", "el-nope"); found {
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
	if n := m.PendingCount("s1"); n != 0 {
		t.Fatalf("取消后挂起表应清空, got %d", n)
	}
}

// TestElicitationScopeMismatch C3 scope 归属：只有登记该提问的会话能决策；
// 跨会话决策不投递、不删除挂起项（found=true 但 mismatch=true，对外按「不存在」处理）。
func TestElicitationScopeMismatch(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	published := make(chan ElicitationItem, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out acl.ElicitationOutcome
	done := make(chan struct{})
	go func() {
		defer close(done)
		out = m.RegisterAndWait(ctx, "owner", acl.ElicitationRequest{
			SessionID: "owner", Mode: "form", Message: "q",
			Schema: &acl.ElicitationSchema{Properties: map[string]any{}},
		}, func(it ElicitationItem) { published <- it })
	}()
	it := <-published
	if it.SessionID != "owner" {
		t.Fatalf("载荷应带归属会话: %+v", it)
	}
	if it.ScopeSource != acl.ScopeInferred {
		t.Fatalf("默认来源应为 inferred_inflight: %q", it.ScopeSource)
	}
	if _, _, ok := m.Owner(it.ID); !ok {
		t.Fatal("Owner 应命中")
	}
	// 他会话决策 → mismatch，且挂起项仍在。
	if found, mismatch := m.ResolveAccept("intruder", it.ID, nil); !found || !mismatch {
		t.Fatalf("跨会话应 mismatch: found=%v mismatch=%v", found, mismatch)
	}
	if found, mismatch := m.ResolveDecline("intruder", it.ID); !found || !mismatch {
		t.Fatalf("跨会话 decline 应 mismatch: found=%v mismatch=%v", found, mismatch)
	}
	if _, _, ok := m.Owner(it.ID); !ok {
		t.Fatal("跨会话决策不得删除挂起项")
	}
	// 归属会话决策 → 正常投递。
	if found, mismatch := m.ResolveCancel("owner", it.ID); !found || mismatch {
		t.Fatalf("归属会话应命中: found=%v mismatch=%v", found, mismatch)
	}
	<-done
	if out.Action != "cancel" {
		t.Fatalf("决策不符: %+v", out)
	}
}

// TestElicitationScopeSourceWire wire 提供 scope 时按 wire 记录（SDK 支持后启用）。
func TestElicitationScopeSourceWire(t *testing.T) {
	m := NewElicitationManager(2 * time.Second)
	published := make(chan ElicitationItem, 1)
	go m.RegisterAndWait(context.Background(), "s9", acl.ElicitationRequest{
		SessionID: "s9", ScopeSource: acl.ScopeWire, Mode: "url", Message: "u", URL: "https://example.com",
	}, func(it ElicitationItem) { published <- it })
	it := <-published
	if it.ScopeSource != acl.ScopeWire {
		t.Fatalf("来源应为 wire: %q", it.ScopeSource)
	}
	_, src, ok := m.Owner(it.ID)
	if !ok || src != acl.ScopeWire {
		t.Fatalf("Owner 来源不符: ok=%v src=%q", ok, src)
	}
}

// TestElicitationTimeoutBecomesPending 超时**不**铸 cancel（ADR-11 / P1-d）。
//
// 四件事一起验，缺一条这一档就名不副实：
//
//	① 回给底座的是 `cancel`（那一轮的 ACP 调用必须有人应答，否则整轮永远挂着）；
//	② 事件说的是 `pending`（卡片保留）；
//	③ 挂起项**还在**，且 `timeout_at` 被清空（"仍可回答"的物理前提）；
//	④ `ResolveLate` 能把它答掉并回问题正文（供调用方作为新一轮注入）。
//
// 为什么单独立一条而不是改既有超时用例：既有那条钉的是"超时会收敛"，而这一批**恰恰是**在
// 取消"超时=收敛"这个等式。把两者混在一条里，改判据的人会以为自己在改同一个断言。
func TestElicitationTimeoutBecomesPending(t *testing.T) {
	m := NewElicitationManager(30 * time.Millisecond)
	type ev struct{ action, reason string }
	events := make(chan ev, 2)
	m.SetOnResolved(func(_, _, action, reason string) { events <- ev{action, reason} })

	var got acl.ElicitationOutcome
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		got = m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
			SessionID: "s1", Mode: "form", Message: "用哪个库？",
		}, nil)
	}()
	wg.Wait()

	// ① 回给底座：cancel（不是 pending —— 底座不认识那个词）。
	if got.Action != "cancel" {
		t.Fatalf("回给底座的 action 应为 cancel（那一轮的 ACP 调用必须有人应答），got %q", got.Action)
	}
	// ② 事件：pending + timeout。
	select {
	case e := <-events:
		if e.action != "pending" || e.reason != "timeout" {
			t.Fatalf("超时事件应为 {pending, timeout}，got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("超时必须发一条 session.elicitation_resolved")
	}
	// ③ 挂起项还在、且不再有超时时刻。
	items := m.PendingItems("s1")
	if len(items) != 1 {
		t.Fatalf("超时后挂起项应保留（卡片可回答的物理前提），got %d 条", len(items))
	}
	if !items[0].TimeoutAt.IsZero() {
		t.Fatalf("超时后 `timeout_at` 应清空（不再起倒计时），got %v", items[0].TimeoutAt)
	}
	// ④ 迟到回答：抹掉挂起项 + 回正文。
	msg, found, mismatch := m.ResolveLate("s1", items[0].ID)
	if !found || mismatch {
		t.Fatalf("PendingItems 报出来的那条应当答得掉: found=%v mismatch=%v", found, mismatch)
	}
	if msg != "用哪个库？" {
		t.Fatalf("ResolveLate 应回问题正文（调用方拿它拼新一轮的用户消息），got %q", msg)
	}
	if n := len(m.PendingItems("s1")); n != 0 {
		t.Fatalf("答掉之后不应再有挂起项，got %d", n)
	}
	// 再答一次：已经没有了（不是幂等成功——那会让界面以为"我答上去了"）。
	if _, found, _ := m.ResolveLate("s1", items[0].ID); found {
		t.Fatal("同一 id 答两次，第二次应 found=false")
	}
}

// TestElicitationResolveLateRejectsEarly 未超时的提问**不**走 late 那一支。
//
// 两条路刻意不混：还在阻塞等的那条回答要投给**那一轮**（`ResolveAccept`），而 late 的语义是
// "那一轮已经不等了"。混起来的表现是回答被当成新一轮、旧轮永远等不到结果。
func TestElicitationResolveLateRejectsEarly(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.RegisterAndWait(context.Background(), "s1", acl.ElicitationRequest{
			SessionID: "s1", Mode: "form", Message: "还在等",
		}, nil)
	}()
	// 等它登记上（RegisterAndWait 是同步登记的，但 goroutine 调度不保证已经跑到）。
	deadline := time.After(2 * time.Second)
	for {
		if items := m.PendingItems("s1"); len(items) == 1 {
			if _, found, _ := m.ResolveLate("s1", items[0].ID); found {
				t.Fatal("未超时的提问不该走 late 那一支（它还在阻塞等决策）")
			}
			// 用常态那一支收尾，避免 goroutine 泄漏。
			m.ResolveCancel("s1", items[0].ID)
			break
		}
		select {
		case <-deadline:
			t.Fatal("挂起项没有登记上来")
		case <-time.After(5 * time.Millisecond):
		}
	}
	wg.Wait()
}
