package manager

import (
	"context"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
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

// TestEventSinkMapping acl 归一事件 → 域事件 type/payload（agg=平台会话 uuid；
// P3 双 id：原生 session id 经 byProvider 解析后发布）。
func TestEventSinkMapping(t *testing.T) {
	_, orch, bus := newTestRig(t)
	orch.handles.BindProvider("s1", "conv-1") // 原生底座 session id → 平台会话 uuid
	sink := orch.runner.EventSink("rtx")

	sink(acl.Event{SessionID: "s1", Kind: acl.KindMessage, Payload: acl.MessageDelta{Text: "hi"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindThought, Payload: acl.ThoughtDelta{Text: "think"}})
	// agent 回传的 user_message 是会话镜像（宿主已在 Prompt 提交时发布 session.BusUserMessage）→ eventSink 丢弃。
	sink(acl.Event{SessionID: "s1", Kind: acl.KindUserMessage, Payload: acl.MessageDelta{Text: "me"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolCall, Payload: acl.ToolCallInfo{ID: "c1", Title: "edit", Status: "pending"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolUpdate, Payload: acl.ToolCallInfo{ID: "c1", Status: "completed"}})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindUsage, Payload: nil})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindPermission, Payload: acl.PermissionInfo{Title: "t", Allowed: false}})
	// 空 session id / 未知 kind → 丢弃。
	sink(acl.Event{SessionID: "", Kind: acl.KindMessage, Payload: acl.MessageDelta{Text: "x"}})
	sink(acl.Event{SessionID: "s1", Kind: "bogus_kind", Payload: "whatever"})

	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)
	var evs []eventbus.Event
	timeout := afterShort()
	// 过程状态（`run.status`）不计入这 6 条：这一条用例钉的是**业务事件的映射与载荷**。
	// 它同时会插一条 `tool_executing`，由 `runstatus_test.go` 单独钉住。
	for len(evs) < 6 {
		select {
		case e := <-sub.C:
			if e.Type == session.BusRunStatus {
				continue
			}
			evs = append(evs, e)
		case <-timeout:
			t.Fatalf("事件不足: got %d want 6, events=%+v", len(evs), evs)
		}
	}
	types := make([]string, 0, len(evs))
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := []string{session.BusMessage, session.BusThought, session.BusToolCall, session.BusToolUpdate, session.BusUsage, "session.permission"}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("type 序列不符: %v", types)
		}
		if evs[i].AggregID != "conv-1" {
			t.Fatalf("agg 不符: %+v", evs[i])
		}
	}
	// 载荷字段（message 的 text、tool_call 的 id/title）。
	m, _ := evs[0].Payload.(map[string]any)
	if m["text"] != "hi" || m["runtime"] != "rtx" {
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
	sink := orch.runner.EventSink("rtx")
	sink(acl.Event{SessionID: "s1", Kind: acl.KindMessage, Payload: "not a delta"})
	sink(acl.Event{SessionID: "s1", Kind: acl.KindToolCall, Payload: nil})
	if bus.Published() != 0 {
		t.Fatalf("类型不匹配不应发布: published=%d", bus.Published())
	}
}

// TestEventSinkCommands 命令发现面（ADR-17）：acl 的 available_commands_update →
// `session.commands` 域事件，agg = 平台会话 uuid，载荷带全量命令且**保序**（平台不重排）。
// 另钉住两条容易做错的语义：空列表**照样发布**（"通告了当前没有命令"要让前端清空候选），
// 载荷类型不匹配则被跳过。
func TestEventSinkCommands(t *testing.T) {
	_, orch, bus := newTestRig(t)
	orch.handles.BindProvider("s1", "conv-1")
	sink := orch.runner.EventSink("rtx")
	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)

	sink(acl.Event{SessionID: "s1", Kind: acl.KindCommands, Payload: acl.CommandsInfo{
		SessionID: "s1",
		Commands: []acl.CommandInfo{
			{Name: "review", Description: "审查改动", Hint: "文件路径"},
			{Name: "init"},
		},
	}})

	var ev eventbus.Event
	select {
	case ev = <-sub.C:
	case <-afterShort():
		t.Fatal("未收到 session.commands 事件")
	}
	if ev.Type != session.BusCommands {
		t.Fatalf("事件 type 不符: %s", ev.Type)
	}
	if ev.AggregID != "conv-1" {
		t.Fatalf("agg 应为平台会话 uuid: %+v", ev)
	}
	p, _ := ev.Payload.(map[string]any)
	if p["session_id"] != "conv-1" || p["runtime"] != "rtx" {
		t.Fatalf("载荷基础字段不符: %+v", p)
	}
	cmds, ok := p["commands"].([]acl.CommandInfo)
	if !ok {
		t.Fatalf("commands 类型不符: %#v", p["commands"])
	}
	if len(cmds) != 2 || cmds[0].Name != "review" || cmds[0].Hint != "文件路径" || cmds[1].Name != "init" {
		t.Fatalf("命令应全量且保序: %+v", cmds)
	}

	// 空列表也要发布：它不是"没发生"，而是"底座通告了当前没有命令"。
	sink(acl.Event{SessionID: "s1", Kind: acl.KindCommands, Payload: acl.CommandsInfo{
		SessionID: "s1", Commands: []acl.CommandInfo{},
	}})
	select {
	case ev = <-sub.C:
	case <-afterShort():
		t.Fatal("空列表通告也必须发布（否则前端会一直显示陈旧候选）")
	}
	p, _ = ev.Payload.(map[string]any)
	if cmds, ok := p["commands"].([]acl.CommandInfo); !ok || len(cmds) != 0 {
		t.Fatalf("空列表载荷不符: %#v", p["commands"])
	}

	// 载荷类型不匹配：跳过而不是发布一个半成品。
	before := bus.Published()
	sink(acl.Event{SessionID: "s1", Kind: acl.KindCommands, Payload: "not commands"})
	if bus.Published() != before {
		t.Fatalf("类型不匹配不应发布: published=%d", bus.Published())
	}
}

// TestDisconnectIdleSession 连接**意外**断开 + 会话空闲：不改写上一轮的终态、不伪造
// run.error（轮次结果只由轮次写）；只落 agent 级诊断（连接确实断了）并作废句柄（可重建）。
func TestDisconnectIdleSession(t *testing.T) {
	svc, orch, bus := rigWithRuntime(t, runtime.Spec{ID: "rtx", Name: "rtx"})
	// 种子 agent 由**内置清单**播种，而这份注册表里只有一个非内置 id（它只是本用例的一个普通
	// 字符串，不是本仓内置清单成员），所以服务该 runtime 的 agent 由夹具显式登记——
	// 字段与 `seedBuiltinAgents` 播出来的那条逐字同形（尤其是 `State: stopped`：后面那条断言
	// 验的是"断连不把 agent 状态改坏"，起点错成空串就变成一句空话）。
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "rtx"}) // client nil
	rc := &runtimeconn.Conn{}                                                      // client 亦 nil → 相等匹配
	orch.handles.Put(as)
	svc.registerSession(&SessionRec{ID: "s1", Status: session.SessionStatusCompleted})
	before := bus.Published()

	orch.pool.Drop(rc)

	if got, _ := svc.sessionPlane.Lookup("s1"); got.Status != session.SessionStatusCompleted {
		t.Fatalf("空闲会话的上一轮终态不应被断连改写: %s", got.Status)
	}
	if bus.Published() != before {
		t.Fatalf("断连不应伪造会话事件（run.error 等）: published=%d want %d", bus.Published(), before)
	}
	if st := svc.runtimePlane.Snapshot().State; st != session.AgentStateStopped {
		t.Fatalf("底座状态应 stopped: %s", st)
	}
	// 句柄作废并摘出活跃集合（下次 prompt 才能重建），平台侧会话记录保留。
	if orch.handles.Get("s1") != nil {
		t.Fatal("断连后死句柄应摘出活跃集合")
	}
	if rec := svc.sessionRec("s1"); rec == nil {
		t.Fatal("平台侧会话记录必须保留（会话归平台所有）")
	}
	// 连接诊断仍是连接事实的来源（v19：单底座，留痕只有一份）。
	if le, ok := svc.runtimeLastError(); !ok || le.Code != "connection_lost" || le.SessionID != "s1" {
		t.Fatalf("底座诊断应记录 connection_lost: %+v", le)
	}

	// 二次 Drop（池）：句柄已 dead → 幂等。
	orch.pool.Drop(rc)
	if bus.Published() != before {
		t.Fatalf("重复断连不应发布事件: published=%d", bus.Published())
	}
}

// TestIntentionalTeardownKeepsIdleSession 主动拆除（daemon 停机 / 空闲回收）+ 空闲会话：
// 停机不是故障 —— 状态如实保留、不发 connection_lost（容器 `docker restart` 的每次重启都会走这条）。
func TestIntentionalTeardownKeepsIdleSession(t *testing.T) {
	svc, orch, bus := rigWithRuntime(t, runtime.Spec{ID: "rtx", Name: "rtx"})
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "rtx"})
	rc := &runtimeconn.Conn{}
	rc.Intentional.Store(true)
	orch.handles.Put(as)
	svc.registerSession(&SessionRec{ID: "s1", Status: session.SessionStatusCompleted})
	before := bus.Published()

	orch.pool.Drop(rc)

	if got, _ := svc.sessionPlane.Lookup("s1"); got.Status != session.SessionStatusCompleted {
		t.Fatalf("主动停机不得改写会话状态（completed 应保持）: %s", got.Status)
	}
	if bus.Published() != before {
		t.Fatalf("主动停机不得发布 connection_lost 等事件: published=%d want %d", bus.Published(), before)
	}
	if orch.handles.Get("s1") != nil {
		t.Fatal("停机后死句柄应摘出活跃集合（下次 prompt 重建）")
	}
}

// TestIntentionalTeardownInterruptsInFlightRun 主动拆除 + 在途 run：归 interrupted（无终态、
// 可续聊重发），既不算失败也不算用户取消 —— 不发布 run.error / run.cancelled。
func TestIntentionalTeardownInterruptsInFlightRun(t *testing.T) {
	svc, orch, bus := rigWithRuntime(t, runtime.Spec{ID: "rtx", Name: "rtx"})
	as := session.NewActiveSession(session.HandleParams{
		ID: "s1", Runtime: "rtx",
		Session: &stubSession{cancelOnCtx: true}, // 阻塞到 ctx 取消，模拟生成中的轮次
	})
	rc := &runtimeconn.Conn{}
	orch.handles.Put(as)
	svc.registerSession(&SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "生成中"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	rc.Intentional.Store(true)
	orch.pool.Drop(rc)
	// 等 executeRun 收尾（t.cancel 解除阻塞 → 写 interrupted）：轮询平台记录，避免裸读 as.run。
	got := ""
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec := svc.sessionRec("s1"); rec != nil {
			if got = rec.Status; got == session.SessionStatusInterrupted {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got != session.SessionStatusInterrupted {
		t.Fatalf("主动拆除打断的轮次应归 interrupted: %s", got)
	}
	// 停机期间只应新增 user_message（Prompt 时发布）；不得有 run.error / run.cancelled。
	// `Subscribe()`（live-only）而不是回放版本：这里要问的是"此后**新**发布了什么"，
	// 回放历史会把上面那一条 user_message 立刻投进来，断言就永远红。
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)
	select {
	case e := <-sub.C:
		t.Fatalf("主动拆除不应发布终态事件: %s", e.Type)
	case <-time.After(200 * time.Millisecond):
	}
	// 再看整条事件日志：live 订阅只能覆盖它创建之后的那一小段，而"收尾时对端先报错"这类
	// 竞态恰好可能落在订阅之前——那一段正是本用例要看的（v21 之前这里用 `LastSeq()` 做
	// 数量断言，位次分会话之后那个量没有意义了，改成按类型/聚合直查）。
	for _, e := range bus.EventsAll() {
		if e.AggregID == "s1" && (e.Type == session.BusRunError || e.Type == session.BusRunCancelled) {
			t.Fatalf("主动拆除不应发布终态事件: %s", e.Type)
		}
	}
	if orch.handles.Get("s1") != nil {
		t.Fatal("停机后死句柄应摘出活跃集合")
	}
}

// TestPublishVersionAndBusNil 无 bus 时 publish 仍递增每 agg 版本、不 panic；事件 Seq=-1（缺号哨兵）。
// 用**非合并类型**（工具卡）验证「每次 publish 一次版本递增」——文本增量走 ③ 合并，
// 号序与版本在 flush 时才分配（下面单独断言这条语义）。
func TestPublishVersionAndBusNil(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "x", Name: "X"})
	svc := NewService(reg, nil) // bus = nil
	e1 := svc.publish(session.BusToolCall, "s1", map[string]any{"tool": "a"})
	e2 := svc.publish(session.BusToolCall, "s1", map[string]any{"tool": "b"})
	_ = svc.publish(session.BusToolCall, "s2", map[string]any{"tool": "c"})
	// Seq 是 -1 而不是 0：v21 起 0 是**真实位次**（没有持久层时 bus 就是号序权威，
	// 但这里 bus=nil，谁都没分配号）——"没号"必须与"第 0 条"区分开。
	if e1.Seq != -1 || e1.Version != 1 || e2.Seq != -1 || e2.Version != 2 {
		t.Fatalf("无 bus publish 不符: %+v %+v", e1, e2)
	}
	if svc.sessionPlane.Version("s1") != 2 || svc.sessionPlane.Version("s2") != 1 {
		t.Fatalf("版本计数不符: s1=%d s2=%d", svc.sessionPlane.Version("s1"), svc.sessionPlane.Version("s2"))
	}
	// ③ 合并：同形文本增量 publish 返回零值事件（未分配号序），flush 后才计入版本。
	if zero := svc.publish(session.BusMessage, "s3", map[string]any{"text": "x"}); zero.Seq != -1 || zero.Version != 0 {
		t.Fatalf("合并中的增量不应分配号序: %+v", zero)
	}
	if svc.sessionPlane.Version("s3") != 0 {
		t.Fatalf("合并中不应递增版本: %d", svc.sessionPlane.Version("s3"))
	}
	svc.flushChunks()
	if svc.sessionPlane.Version("s3") != 1 {
		t.Fatalf("flush 后应递增版本: %d", svc.sessionPlane.Version("s3"))
	}
}

// TestSessionHistoryClamp service 层参数夹取：after<-1→-1（"从头"哨兵）、limit<1→1。
func TestSessionHistoryClamp(t *testing.T) {
	svc, _, _ := newTestRig(t)
	svc.registerSession(&SessionRec{ID: "s1"})
	// 两条不同消息（msg_id 不同 → 形状不同 → 不合并），分页断言才成立。
	svc.publish(session.BusMessage, "s1", map[string]any{"msg_id": "m1", "text": "one"})
	svc.publish(session.BusMessage, "s1", map[string]any{"msg_id": "m2", "text": "two"})
	svc.flushChunks()

	// `-5` 是"比任何位次都小"的显式游标（服务端夹到哨兵 -1 = 从头）；limit 0 → 1。
	p, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(-5), Limit: 0})
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
	_, err := svc.CreateSession(context.Background(), dtosession.SessionCreateReq{Prompt: "hi"})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 50201 {
		t.Fatalf("期望 50201，got %v", err)
	}
	if _, err := svc.PromptSession(context.Background(), "s1", dtosession.PromptReq{Prompt: "x"}); err == nil {
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
	_, err := svc.PromptSession(context.Background(), "ghost", dtosession.PromptReq{Prompt: "hi"})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40401 {
		t.Fatalf("期望 40401，got %v", err)
	}
}

// TestServiceRESTValidation Prompt 必填校验（service 层，无 orch 前置）。
func TestServiceRESTValidation(t *testing.T) {
	svc, _, _ := newTestRig(t)
	if _, err := svc.PromptSession(context.Background(), "", dtosession.PromptReq{}); err == nil {
		t.Fatal("空 id/prompt 应报错")
	}
}

// TestStartSessionUnknownRuntime 构建期底座未装配 → runtime_not_found（拨号前拦截）。
//
// 历史：这里曾种一个"指向未注册 runtime 的 agent"（agent 可声明候选的年代），以及"未知 agent →
// agent_not_found"那条用例。两处都随 agent 面一起下线：v19 里唯一能让底座解析失败的形态只剩
// "构建期选定的那个不在注册表里"。
func TestStartSessionUnknownRuntime(t *testing.T) {
	_, orch, _ := newTestRig(t) // 空注册表 → 构建期底座（默认 dsh）必然未装配
	_, err := orch.StartSession(context.Background(), StartOptions{Cwd: "/tmp"})
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "runtime_not_found" {
		t.Fatalf("期望 runtime_not_found，got %v", err)
	}
}

// TestRunOnceUnregisteredRuntime 构建期选定的 runtime 不在注册表里时直接报错
// （正常路径由装配期的 ProductRegistry fail-closed 拦住，这里是兜底判据）。
func TestRunOnceUnregisteredRuntime(t *testing.T) {
	_, orch, _ := newTestRig(t)
	if _, err := orch.RunOnce(context.Background(), RunOptions{Prompt: "x"}); err == nil {
		t.Fatal("构建期 runtime 未注册应报错")
	}
}

// TestServiceResolveRuntimeUsesBuildRuntime 底座解析只剩一步：取构建期常量并校验它已装配。
//
// 历史：这里曾是 FirstAvailable / CapabilityRouted 路由引擎的用例（首候选不可执行 → 取次候选、
// runtime_unavailable / no_runtime …）。用户不能选择 runtime 之后，路由的每个输入都成了常量，
// 那些码与分支整体不存在了；留下的唯一失败面是"构建期选定的那个没装配"。
func TestServiceResolveRuntimeUsesBuildRuntime(t *testing.T) {
	// 已装配：原样返回构建期常量（不是"注册表里第一个"，也不是 agent 的候选）。
	svc, _, _ := rigWithRuntime(t, runtime.Spec{ID: "stub", Name: "Stub"})
	setBuildRuntime(t, "stub")
	if id, err := svc.runtimePlane.Resolve(); err != nil || id != "stub" {
		t.Fatalf("应返回构建期底座 stub，id=%q err=%v", id, err)
	}

	// 未装配：明确报 runtime_not_found（不静默回落到注册表里的别的底座）。
	empty := NewService(runtime.NewRegistry(), nil)
	setBuildRuntime(t, "stub")
	_, err := empty.runtimePlane.Resolve()
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "runtime_not_found" {
		t.Fatalf("空注册表应报 runtime_not_found，got %v", err)
	}
}

// TestPublishTitleConcurrentUpdate 并发下 session.BusUserMessage 标题自动推导与 PATCH 覆盖标题
// 都须持锁（旧实现 publish 在锁外读写 Title → 数据竞争）；-race 下本用例作为守门测试。
func TestPublishTitleConcurrentUpdate(t *testing.T) {
	svc, _, _ := newTestRig(t)
	id := "s-title-race"
	svc.mu.Lock()
	svc.sessionPlane.RegisterSession(&SessionRec{ID: id, Status: session.SessionStatusInProgress})
	svc.sessionPlane.ResetVersion(id)
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
				_ = svc.publish(session.BusUserMessage, id, map[string]any{
					"session_id": id, "runtime": "rtx", "text": fmt.Sprintf("消息 %d-%d", n, j),
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
				_, _ = metaSvc(svc).UpdateSession(context.Background(), id, dtosession.SessionPatch{Title: &title})
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
