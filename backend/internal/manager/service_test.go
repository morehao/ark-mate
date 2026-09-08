package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/gerror"
)

// afterShort 短超时（事件等待防挂测）。
func afterShort() <-chan time.Time { return time.After(3 * time.Second) }

// rigWithRuntime 构造带指定 runtime 的 svc+orch+bus（可种子 agent）。
func rigWithRuntime(t *testing.T, specs ...runtime.Spec) (*Service, *Orchestrator, *eventbus.Bus) {
	t.Helper()
	reg := runtime.NewRegistry(specs...)
	bus := eventbus.New()
	svc := NewService(reg, bus)
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return svc, orch, bus
}

// TestRestErrMapping 领域错误（acl 分类）→ 对外业务码映射矩阵。
func TestRestErrMapping(t *testing.T) {
	svc, _, _ := newTestRig(t)
	cases := []struct {
		name string
		in   error
		want int
	}{
		{"agent_not_found", acl.NewError(acl.ClassRequest, "agent_not_found", "x", nil), 40401},
		{"session_not_found", acl.NewError(acl.ClassRequest, "session_not_found", "x", nil), 40401},
		{"runtime_not_found", acl.NewError(acl.ClassRequest, "runtime_not_found", "x", nil), 40401},
		{"request_other", acl.NewError(acl.ClassRequest, "agent_required", "x", nil), 40001},
		{"limit", acl.NewError(acl.ClassLimit, "turn_in_progress", "x", nil), 40901},
		{"access", acl.NewError(acl.ClassAccess, "auth_required", "x", nil), 40101},
		{"unsupported", acl.NewError(acl.ClassUnsupported, "resume_unsupported", "x", nil), 40501},
		{"service", acl.NewError(acl.ClassService, "orchestrator_unavailable", "x", nil), 50201},
		{"connection", acl.NewError(acl.ClassConnection, "connection_lost", "x", nil), 50201},
		{"unknown_class", acl.NewError(acl.ClassUnknown, "weird", "x", nil), 50001},
		{"plain_error", errors.New("boom"), 50000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ge gerror.Error
			if !errors.As(svc.restErr(tc.in), &ge) || ge.Code != tc.want {
				t.Fatalf("映射不符: %v → %d (want %d)", tc.in, ge.Code, tc.want)
			}
		})
	}

	// details 为空 → msg 回落为 code。
	var ge gerror.Error
	_ = errors.As(svc.restErr(acl.NewError(acl.ClassLimit, "busy", "", nil)), &ge)
	if ge.Msg != "busy" {
		t.Fatalf("空 details 应回落 code: %+v", ge)
	}
}

// TestEventSinkMapping acl 归一事件 → 域事件 type/payload（agg=session id）。
func TestEventSinkMapping(t *testing.T) {
	_, orch, bus := newTestRig(t)
	sink := orch.eventSink("agent-opencode", "opencode")

	sink(acl.Event{SessionID: "s1", Kind: acl.KindMessage, Payload: acl.MessageDelta{Text: "hi"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindThought, Payload: acl.ThoughtDelta{Text: "think"}})
	// agent 回传的 user_message 是会话镜像（宿主已在 Prompt 提交时发布 BusUserMessage）→ eventSink 丢弃。
	sink(acl.Event{SessionID: "s1", Kind: acl.KindUserMessage, Payload: acl.MessageDelta{Text: "me"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolCall, Payload: acl.ToolCallInfo{ID: "c1", Title: "edit", Status: "pending"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolUpdate, Payload: acl.ToolCallInfo{ID: "c1", Status: "completed"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindUsage, Payload: nil})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindPermission, Payload: acl.PermissionInfo{Title: "t", Allowed: false}})
	// 空 session id / 未知 kind → 丢弃。
	sink(acl.Event{SessionID: "", Kind: acl.KindMessage, Payload: acl.MessageDelta{Text: "x"}})
	sink(acl.Event{SessionID: "s1", Kind: "bogus_kind", Payload: "whatever"})

	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	var evs []eventbus.Event
	timeout := afterShort()
	for len(evs) < 6 {
		select {
		case e := <-sub.C:
			evs = append(evs, e)
		case <-timeout:
			t.Fatalf("事件不足: got %d want 6, events=%+v", len(evs), evs)
		}
	}
	types := make([]string, 0, len(evs))
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := []string{BusMessage, BusThought, BusToolCall, BusToolUpdate, BusUsage, "session.permission"}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("type 序列不符: %v", types)
		}
		if evs[i].AggregID != "s1" {
			t.Fatalf("agg 不符: %+v", evs[i])
		}
	}
	// 载荷字段（message 的 text、tool_call 的 id/title）。
	m, _ := evs[0].Payload.(map[string]any)
	if m["text"] != "hi" || m["agent_id"] != "agent-opencode" {
		t.Fatalf("message payload 不符: %+v", evs[0].Payload)
	}
	tc, _ := evs[2].Payload.(map[string]any)
	if tc["tool_call_id"] != "c1" || tc["title"] != "edit" {
		t.Fatalf("tool_call payload 不符: %+v", evs[2].Payload)
	}
	perm, _ := evs[5].Payload.(acl.PermissionInfo)
	if perm.Title != "t" || perm.Allowed {
		t.Fatalf("permission payload 应透传: %+v", evs[5].Payload)
	}
}

// TestEventSinkWrongPayloadType 载荷类型不匹配 → 事件被跳过（不 panic、不发布）。
func TestEventSinkWrongPayloadType(t *testing.T) {
	_, orch, bus := newTestRig(t)
	sink := orch.eventSink("a", "r")
	sink(acl.Event{SessionID: "s1", Kind: acl.KindMessage, Payload: "not a delta"})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolCall, Payload: nil})
	if bus.LastSeq() != 0 {
		t.Fatalf("类型不匹配不应发布: last=%d", bus.LastSeq())
	}
}

// TestDisconnectIdleSession 连接断开时空闲会话 → turn.error(connection_lost) + failed + agent stopped，
// 且幂等（只发一次）。
func TestDisconnectIdleSession(t *testing.T) {
	svc, orch, bus := rigWithRuntime(t, runtime.Spec{ID: "opencode", Name: "opencode"})
	as := &ActiveSession{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode"} // client nil
	rc := &runtimeConn{agentID: "agent-opencode", client: nil}                     // client 亦 nil → 相等匹配
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode", Status: SessionStatusInProgress})

	orch.dropConn(rc)

	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	var evs []eventbus.Event
	timeout := afterShort()
	for len(evs) < 1 {
		select {
		case e := <-sub.C:
			evs = append(evs, e)
		case <-timeout:
			t.Fatalf("未收到 connection_lost 事件")
		}
	}
	if evs[0].Type != BusTurnError {
		t.Fatalf("应发布 turn.error，got %s", evs[0].Type)
	}
	payload, _ := evs[0].Payload.(map[string]any)
	if payload["code"] != "connection_lost" || payload["class"] != "connection" {
		t.Fatalf("错误分类不符: %+v", payload)
	}
	if svc.sessions["s1"].Status != SessionStatusFailed {
		t.Fatalf("会话应 failed: %s", svc.sessions["s1"].Status)
	}
	if a, _ := svc.AgentByID("agent-opencode"); a.State != AgentStateStopped {
		t.Fatalf("agent 应 stopped: %s", a.State)
	}

	// 二次 dropConn：as.dead=true → 幂等不重复发布。
	orch.dropConn(rc)
	if bus.LastSeq() != evs[0].Seq {
		t.Fatalf("断连错误不应重复发布: last=%d want %d", bus.LastSeq(), evs[0].Seq)
	}
}

// TestPublishVersionAndBusNil 无 bus 时 publish 仍递增每 agg 版本、不 panic；事件 Seq=0。
func TestPublishVersionAndBusNil(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "x", Name: "X"})
	svc := NewService(reg, nil) // bus = nil
	_ = svc.publish(BusMessage, "s1", map[string]any{"text": "a"})
	e2 := svc.publish(BusMessage, "s1", map[string]any{"text": "b"})
	_ = svc.publish(BusMessage, "s2", map[string]any{"text": "c"})
	if e2.Seq != 0 || e2.Version != 2 {
		t.Fatalf("无 bus publish 不符: %+v", e2)
	}
	if svc.ver["s1"] != 2 || svc.ver["s2"] != 1 {
		t.Fatalf("版本计数不符: %v", svc.ver)
	}
}

// TestSessionHistoryClamp service 层参数夹取：after<0→0、limit<1→1。
func TestSessionHistoryClamp(t *testing.T) {
	svc, _, _ := newTestRig(t)
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "a", Runtime: "r"})
	svc.publish(BusMessage, "s1", map[string]any{"text": "one"})
	svc.publish(BusMessage, "s1", map[string]any{"text": "two"})

	p, err := svc.SessionHistory(context.Background(), "s1", -5, 0)
	if err != nil {
		t.Fatalf("SessionHistory: %v", err)
	}
	if len(p.Events) != 1 { // limit 0→1
		t.Fatalf("limit 夹取不符: %+v", p)
	}
	if p.Events[0].Payload.(map[string]any)["text"] != "one" {
		t.Fatalf("after 夹取应从头回放: %+v", p)
	}
}

// TestCreateSessionWithoutOrchestrator 未装配编排器 → 50201 业务错误。
func TestCreateSessionWithoutOrchestrator(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "x", Name: "X"})
	svc := NewService(reg, eventbus.New()) // 无 orch
	_, err := svc.CreateSession(context.Background(), handler.SessionCreateReq{AgentID: "agent-x", Prompt: "hi"})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 50201 {
		t.Fatalf("期望 50201，got %v", err)
	}
	if _, err := svc.PromptSession(context.Background(), "s1", "x"); err == nil {
		t.Fatal("无 orch 应报错")
	} else {
		var ge2 gerror.Error
		if !errors.As(err, &ge2) || ge2.Code != 50201 {
			t.Fatalf("期望 50201，got %+v", err)
		}
	}
}

// TestPromptSessionUnknown 既有编排器下未知会话 → 40401。
func TestPromptSessionUnknown(t *testing.T) {
	svc, _, _ := newTestRig(t)
	_, err := svc.PromptSession(context.Background(), "ghost", "hi")
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40401 {
		t.Fatalf("期望 40401，got %v", err)
	}
}

// TestServiceRESTValidation Prompt 必填校验（service 层，无 orch 前置）。
func TestServiceRESTValidation(t *testing.T) {
	svc, _, _ := newTestRig(t)
	if _, err := svc.PromptSession(context.Background(), "", ""); err == nil {
		t.Fatal("空 id/prompt 应报错")
	}
}

// TestStartSessionUnknownAgent 未知 agent → agent_not_found（拨号前拦截，不发网络）。
func TestStartSessionUnknownAgent(t *testing.T) {
	_, orch, _ := newTestRig(t)
	_, err := orch.StartSession(context.Background(), "ghost-agent", "/tmp")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "agent_not_found" {
		t.Fatalf("期望 agent_not_found，got %v", err)
	}
}

// TestStartSessionUnknownRuntime agent 指向未注册 runtime → runtime_not_found（拨号前拦截）。
func TestStartSessionUnknownRuntime(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	// 手工种一个指向未注册 runtime 的 agent（真实场景来自配置漂移）。
	svc.mu.Lock()
	svc.agents["agent-ghost"] = &AgentRec{ID: "agent-ghost", Name: "ghost", Runtime: "not-registered"}
	svc.mu.Unlock()
	_, err := orch.StartSession(context.Background(), "agent-ghost", "/tmp")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "runtime_not_found" {
		t.Fatalf("期望 runtime_not_found，got %v", err)
	}
}

// TestRunOnceUnknownRuntime demo 路径未知 runtime 直接报错。
func TestRunOnceUnknownRuntime(t *testing.T) {
	_, orch, _ := newTestRig(t)
	if _, err := orch.RunOnce(context.Background(), RunOptions{RuntimeID: "nope", Prompt: "x"}); err == nil {
		t.Fatal("未知 runtime 应报错")
	}
}

// TestPublishTitleConcurrentUpdate 并发下 BusUserMessage 标题自动推导与 PATCH 覆盖标题
// 都须持锁（旧实现 publish 在锁外读写 Title → 数据竞争）；-race 下本用例作为守门测试。
func TestPublishTitleConcurrentUpdate(t *testing.T) {
	svc, _, _ := newTestRig(t)
	id := "s-title-race"
	svc.mu.Lock()
	svc.sessions[id] = &SessionRec{ID: id, AgentID: "agent-opencode", Runtime: "echo", Status: SessionStatusInProgress}
	svc.ver[id] = 0
	svc.mu.Unlock()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = svc.publish(BusUserMessage, id, map[string]any{
					"session_id": id, "agent_id": "agent-opencode", "text": fmt.Sprintf("消息 %d-%d", n, j),
				})
			}
		}(i)
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				select {
				case <-stop:
					return
				default:
				}
				title := fmt.Sprintf("PATCH 标题 %d-%d", n, j)
				_, _ = svc.UpdateSession(context.Background(), id, handler.SessionPatch{Title: &title})
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		close(stop)
		t.Fatal("并发守门测试超时（疑似死锁）")
	}
	// 不做终值断言：并发写序不定，仅守门数据竞争/panic。
}
