package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/golib/gerror"
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
	if m.pendingCount(it.SessionID) != 0 {
		t.Fatal("决策后挂起表应清空")
	}
}

// TestElicitationCompleteByURLID verify agent 的 elicitation/complete 收尾（URL 模式）：
// 按 ACP 侧 elicitationId 命中挂起项 → 以 accept + reason=complete 收敛（不再挂到超时）；
// 归属不符/未知 id/非 URL 模式/重放一律无副作用。
func TestElicitationCompleteByURLID(t *testing.T) {
	m := NewElicitationManager(5 * time.Second)
	resolved := make(chan [2]string, 2)
	m.SetOnResolved(func(_, _ string, reason string, d acl.ElicitationOutcome) {
		resolved <- [2]string{d.Action, reason}
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

// TestResolveElicitationScope C3 归属判定三分支：wire（`_meta`）优先 → inferred_inflight（唯一在途
// run）→ ambiguous（都没有，返回空 id 由调用方按取消回应并留痕）。cli=nil 时无在途 run，
// 正好覆盖后两个分支（wire 分支不碰 cli）。
//
// v15（D-4 修复）之后 wire 分支的口径变了：`_meta` 里的 id 是**底座原文**，必须先按
// `runtime_session_id` 反查平台会话；查不到就退 ambiguous —— 这正是本用例现在要钉的东西
// （改之前它会把 `wire-conv` 直接当成平台会话 id 用，把提问卡挂到一个不存在的会话上）。
func TestResolveElicitationScope(t *testing.T) {
	_, orch, _ := newTestRig(t)

	// 先登记一条会话，让它的 `runtime_session_id` 是 `rt_wire-sess`。
	orch.svc.registerSession(&SessionRec{
		ID: "ses_platform", Cwd: "/tmp",
		RuntimeSessionID: objid.RuntimeShell + "wire-sess",
	})

	if sid, src, note := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		SessionID: "wire-sess", ScopeSource: acl.ScopeWire,
	}); sid != "ses_platform" || src != acl.ScopeWire || note != "" {
		t.Fatalf("wire 归属应按 runtime_session_id 反查成平台会话: sid=%q src=%q note=%q", sid, src, note)
	}
	// **查不到**的 wire id（底座发了个我们不认识的会话）→ ambiguous + 留痕，**不猜**。
	if sid, src, note := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		SessionID: "wire-unknown", ScopeSource: acl.ScopeWire,
	}); sid != "" || src != acl.ScopeAmbiguous || note == "" {
		t.Fatalf("查不到的 wire 归属应落 ambiguous 且留痕: sid=%q src=%q note=%q", sid, src, note)
	}
	// ScopeSource=wire 但 SessionID 为空（脏数据）→ 不认，退到推断/歧义。
	if sid, src, _ := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		ScopeSource: acl.ScopeWire,
	}); sid != "" || src != acl.ScopeAmbiguous {
		t.Fatalf("空 wire 归属应落 ambiguous: sid=%q src=%q", sid, src)
	}
	if sid, src, _ := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		Mode: "form", Message: "q",
	}); sid != "" || src != acl.ScopeAmbiguous {
		t.Fatalf("无 wire 且无在途 run 应落 ambiguous: sid=%q src=%q", sid, src)
	}
}

// TestRuntimeDiagnosticsShowsScopeAmbiguous verify 归属歧义不再无痕：留痕随底座诊断的
// remediation 外露，给出事实 + 可执行动作（否则用户只觉得「提问卡没出现」）。
func TestRuntimeDiagnosticsShowsScopeAmbiguous(t *testing.T) {
	svc, _ := newSearchRig(t)
	svc.noteScopeAmbiguous("elicitation 无法归属会话（wire `_meta` 未给 sessionId，且连接上无唯一在途 run）")

	resp, err := svc.RuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("RuntimeDiagnostics: %v", err)
	}
	joined := strings.Join(resp.Remediation, "\n")
	if !strings.Contains(joined, "无法归属会话") {
		t.Fatalf("归属歧义应出现在 remediation: %+v", resp.Remediation)
	}
	if !strings.Contains(joined, "_meta") {
		t.Fatalf("remediation 应给出可执行动作（_meta 带 sessionId）: %+v", resp.Remediation)
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
	if n := m.pendingCount("s1"); n != 0 {
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

// TestServiceResolveElicitationScope C3：REST 面（service）跨会话决策按「不存在」处理——
// 与既有「未知 elicitation_id」完全同码同文案（40001，不泄露他会话挂起项的存在）；
// 归属会话决策成功；列出挂起项时带 session_id/scope_source。
func TestServiceResolveElicitationScope(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	svc.registerSession(&SessionRec{ID: "owner"})
	svc.registerSession(&SessionRec{ID: "intruder"})

	published := make(chan ElicitationItem, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		orch.elic.RegisterAndWait(context.Background(), "owner", acl.ElicitationRequest{
			SessionID: "owner", Mode: "form", Message: "q",
			Schema: &acl.ElicitationSchema{Properties: map[string]any{}},
		}, func(it ElicitationItem) { published <- it })
	}()
	it := <-published

	list, err := svc.ListPendingElicitations(context.Background(), "owner")
	if err != nil {
		t.Fatalf("ListPendingElicitations: %v", err)
	}
	if len(list) != 1 || list[0].SessionID != "owner" || list[0].ScopeSource == "" {
		t.Fatalf("挂起项应带 scope 归属: %+v", list)
	}
	if other, err := svc.ListPendingElicitations(context.Background(), "intruder"); err != nil || len(other) != 0 {
		t.Fatalf("他会话不应看到挂起项: %+v err=%v", other, err)
	}

	err = svc.ResolveElicitation(context.Background(), "intruder", it.ID, "accept", map[string]any{"k": "v"})
	if err == nil {
		t.Fatal("跨会话决策应报错")
	}
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40001 {
		t.Fatalf("跨会话决策期望 40001（与未知 id 同码），got %v", err)
	}
	if !strings.Contains(ge.Msg, "不存在或已过期") {
		t.Fatalf("跨会话文案应与未知 id 一致: %q", ge.Msg)
	}
	if _, _, ok := orch.elic.Owner(it.ID); !ok {
		t.Fatal("跨会话决策不得清除挂起项")
	}

	if err := svc.ResolveElicitation(context.Background(), "owner", it.ID, "accept", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("归属会话决策应成功: %v", err)
	}
	<-done // 等待 RegisterAndWait 消费决策并清表（异步删除）
	if _, _, ok := orch.elic.Owner(it.ID); ok {
		t.Fatal("决策后应清除挂起项")
	}
	// 未知会话仍是 40401（会话不存在，先于 elicitation 校验）。
	if err := svc.ResolveElicitation(context.Background(), "ghost", it.ID, "accept", nil); err == nil {
		t.Fatal("未知会话应报错")
	}
}
