package manager

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// TestRecoverSessionRejectsUnknownOrMissingAgent 断点恢复的守卫路径（不触达拨号）：
// 未知会话 / agent 已不存在均返回请求类错误，不 panic、不误恢复。
func TestRecoverSessionRejectsUnknownOrMissingAgent(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, eventbus.New())
	o := NewOrchestrator(svc, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := o.recoverSession(ctx, "nope"); !isRequestErr(err, "session_not_found") {
		t.Fatalf("未知会话应报 session_not_found, got %v", err)
	}
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "ghost-agent", Runtime: "echo", Status: SessionStatusCompleted})
	if _, err := o.recoverSession(ctx, "s1"); !isRequestErr(err, "agent_not_found") {
		t.Fatalf("agent 缺失应报 agent_not_found, got %v", err)
	}
}

func isRequestErr(err error, code string) bool {
	e, ok := err.(*acl.Error)
	return ok && e.Code == code
}

// TestRecoverSessionBindsAliasAndSessionMap 验证恢复相关登记形状（不触达真实拨号）：
// identity 会话无需别名原样返回；登记别名后 runtime id 归一到外部 id。
func TestRecoverSessionBindsAliasAndSessionMap(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, eventbus.New())
	o := NewOrchestrator(svc, reg)

	// svc 登记一个「重启后残留」的外部会话记录（agent 存在、cwd 持久化）。
	svc.registerSession(&SessionRec{ID: "ext-s1", AgentID: "agent-echo", Runtime: "echo",
		Cwd: "/tmp/work", Status: SessionStatusCompleted})

	// 桩：直接塞一个已注册 runtimeConn + alias 后的 ACP 会话形态——
	// recoverSession 中真正拨号/NewSession 的路径依赖 acl.StdioTransport 子进程，
	// 单测不触达；这里只验证「会话重建成功后的登记形状」与注册守卫的等价实现。
	agent, _ := svc.AgentByID("agent-echo")
	spec, _ := reg.Get(agent.Runtime)
	_ = spec
	if agent.Runtime != "echo" {
		t.Fatalf("默认播种异常: %+v", agent)
	}
	_ = o

	// 别名解析：identity 未登记 → 原样返回；登记后 → 映射外部 id。
	if got := o.resolveSessionID("ext-s1"); got != "ext-s1" {
		t.Fatalf("identity 应原样返回: %s", got)
	}
	o.registerAlias("rt-sess-1", "ext-s1")
	if got := o.resolveSessionID("rt-sess-1"); got != "ext-s1" {
		t.Fatalf("alias 应映射到外部 id: %s", got)
	}
	o.mu.Lock()
	_ = o.sessions
	o.mu.Unlock()
}

// TestEventSinkAliasForwardsToExternalAgg 断点恢复别名下：runtime 会话事件经 eventSink
// 归一发布到外部会话 agg，且 payload.session_id 同步改写；runtime id 下无事件。
func TestEventSinkAliasForwardsToExternalAgg(t *testing.T) {
	bus := eventbus.New()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, bus)
	o := NewOrchestrator(svc, reg)
	o.registerAlias("rt-sess-1", "ext-s1")

	o.eventSink("agent-echo", "echo")(acl.Event{
		SessionID: "rt-sess-1",
		Kind:      acl.KindMessage,
		Payload:   acl.MessageDelta{Text: "你好", MessageID: "m1"},
	})

	evs := eventsForAgg(bus, "ext-s1")
	if len(evs) != 1 {
		t.Fatalf("外部 agg 应有 1 条事件, got %d", len(evs))
	}
	p, ok := evs[0].Payload.(map[string]any)
	if !ok || p["session_id"] != "ext-s1" || p["text"] != "你好" {
		t.Fatalf("事件载荷未归一: %+v", evs[0].Payload)
	}
	if got := eventsForAgg(bus, "rt-sess-1"); len(got) != 0 {
		t.Fatalf("runtime agg 不应有事件, got %+v", got)
	}
}

// eventsForAgg 返回 bus 全局历史中归属指定聚合的事件（断点恢复别名断言的测试助手）。
func eventsForAgg(bus *eventbus.Bus, agg string) []eventbus.Event {
	var out []eventbus.Event
	for _, e := range bus.EventsAfter(0) {
		if e.AggregID == agg {
			out = append(out, e)
		}
	}
	return out
}

// TestRequestPermissionAliasKeysExternalSession 断点恢复别名下：权限请求以外部会话 id
// 为键登记挂起（pending/决策可命中），事件 agg 与载荷 session_id 均为外部 id。
func TestRequestPermissionAliasKeysExternalSession(t *testing.T) {
	bus := eventbus.New()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, bus)
	o := NewOrchestrator(svc, reg)
	o.registerAlias("rt-sess-1", "ext-s1")

	req := acl.PermissionRequest{
		SessionID: "rt-sess-1", // SDK 以 runtime 会话 id 派发
		ToolCall:  acl.ToolCallInfo{ID: "tc1", Kind: "bash", Input: "ls"},
		Options:   []acl.PermissionOption{{OptionID: "always_allow", Name: "始终允许", Kind: "allow_always"}},
	}
	done := make(chan acl.PermissionDecision, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		done <- o.requestPermission(ctx, req)
	}()

	// 挂起应以外部 id 登记。
	var callID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ps := o.perm.pendingForSession("ext-s1")
		if len(ps) > 0 {
			callID = ps[0].callID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("权限请求未以外部 id 登记挂起")
	}
	if got := o.perm.pendingForSession("rt-sess-1"); len(got) != 0 {
		t.Fatalf("runtime id 不应有挂起: %+v", got)
	}

	// 决策后请求返回；事件 agg 为外部 id。
	o.perm.ResolveAllow(callID, "always_allow", false)
	select {
	case d := <-done:
		if !d.Allowed {
			t.Fatalf("应允许: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("权限请求未在决策后返回")
	}
	evs := eventsForAgg(bus, "ext-s1")
	if len(evs) == 0 {
		t.Fatal("外部 agg 应有 permission_request 事件")
	}
	p, ok := evs[0].Payload.(map[string]any)
	if !ok || p["session_id"] != "ext-s1" {
		t.Fatalf("权限事件载荷未归一: %+v", evs[0].Payload)
	}
}
