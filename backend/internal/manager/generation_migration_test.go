package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// ——— M3-6：轮次边界的代际迁移 ———
//
// `generation_test.go` 管的是**代际键与连接池**（M1：键怎么算、怎么复用、怎么一次性关掉）；
// 本文件管的是**会话在轮次边界换不换代际**（M3-6 的四条纪律）。
//
// 判据全部落在**可观察的事实**上（句柄指针、`conns` 条数、库里的指纹、session/new 真的收到了
// 什么、事件里的 reason），而不是"决策函数返回了什么"——决策再对，句柄没换也等于没迁。

// genRig 迁移用例的架子：stub runtime + sqlite 库 + 一个名为 a1 的 agent。
//
// 带库是有意的：**指纹换没换最终以库里的行为准**（重启后按它决定挂哪个代际），
// 只在内存里换等于下次重启又漂回去。
type genRig struct {
	svc  *Service
	orch *Orchestrator
	bus  *eventbus.Bus
	dir  string // 能力目录（定义读写的事实源）
}

func newGenRig(t *testing.T) *genRig {
	t.Helper()
	spec := stubACPSpec(t)
	spec.McpTransports = []string{"stdio"} // 桩只承载 stdio（保守形态）
	reg := runtime.NewRegistry(spec)
	bus := eventbus.New()
	svc := NewService(reg, bus)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc.EnableStore(st)
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Runtimes: []string{"stub"}, State: AgentStateStopped}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return &genRig{svc: svc, orch: orch, bus: bus, dir: t.TempDir()}
}

// registerMcp 往注册表里放一个 stdio MCP（模拟 `mcp/*.json` 装载/改动）。
func (r *genRig) registerMcp(t *testing.T, m capability.McpServer) {
	t.Helper()
	if err := r.svc.caps.RegisterMcpServer(&m); err != nil {
		t.Fatalf("注册 MCP %s: %v", m.ID, err)
	}
}

// sessionNewLog 让桩 runtime 把每次 session/new 收到的参数落盘，返回读取函数。
//
// 断言"新代际真的按新能力开会话"只能看底座收到了什么——管理侧算得再对，
// 下发的若是旧集合，用户拿到的就是旧能力（这正是 M3 要消灭的那类静默失败）。
func sessionNewLog(t *testing.T) func() []map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-new.jsonl")
	t.Setenv("STUB_ACP_LOG", path)
	return func() []map[string]any {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var out []map[string]any
		for _, line := range strings.Split(string(raw), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				out = append(out, m)
			}
		}
		return out
	}
}

// startedConversation 建一条钉住 selected 的会话，返回 id 与当时的句柄。
func (r *genRig) startedConversation(t *testing.T, cwd string, sel *CapabilitySelection) (string, *ActiveConversation) {
	t.Helper()
	agent, _ := r.svc.AgentByID("a1")
	res, err := r.orch.startConversationOn(context.Background(), agent, "stub", StartOptions{Cwd: cwd, Selection: sel})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	as := r.orch.conversation(res.ConversationID)
	if as == nil {
		t.Fatal("建会话后应有活句柄")
	}
	return res.ConversationID, as
}

// TestGenerationMigration 轮次边界的迁移：指纹真变了才切、只按白名单内的变化切、
// 换句柄/换连接/落库/事件/注入五件事一起发生。
func TestGenerationMigration(t *testing.T) {
	r := newGenRig(t)
	log := sessionNewLog(t)
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v1"}})
	r.registerMcp(t, capability.McpServer{ID: "git", Name: "git", Transport: capability.TransportStdio, Command: "/bin/echo"})
	cwd := t.TempDir()

	// 钉住「只要 fs」：git 在注册表里、也在默认集里，但不在这条会话的白名单里。
	convID, as1 := r.startedConversation(t, cwd, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	if as1.Fingerprint == "" {
		t.Fatal("钉住选定的会话必须有指纹")
	}

	// ① 同一套定义再取句柄 → **同一个**句柄（绝不每轮切）。这是最常见的一轮，
	//    它必须零开销：连"要不要切"的判断都不该换句柄。
	as2 := r.orch.maybeMigrateGeneration(context.Background(), as1)
	if as2 != as1 {
		t.Fatal("指纹未变时不得换代际")
	}

	// ① 的强化：加一个**不在白名单里**的 MCP → 指纹不变 → 仍不切。
	//    "只纳入真正会生效的东西"这条纪律要是破了，用户加一个跟自己无关的 server
	//    就会把在跑的会话换一次代际（白丢上下文）。
	r.registerMcp(t, capability.McpServer{ID: "extra", Name: "extra", Transport: capability.TransportStdio, Command: "/bin/echo"})
	if as3 := r.orch.maybeMigrateGeneration(context.Background(), as1); as3 != as1 {
		t.Fatal("白名单外的定义变化不得换代际（它对这个会话的能力集没有任何影响）")
	}

	// ② 真的改了白名单内的定义（fs 的 command）→ 指纹变了 → 本轮切。
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v2"}})
	agent, _ := r.svc.AgentByID("a1")
	sel := ParseCapabilitySelection(r.svc.conversationRec(convID).CapabilityJSON)
	want := r.svc.capabilityFingerprint(agent, "stub", sel)
	if want == as1.Fingerprint {
		t.Fatal("改了白名单内定义却没换指纹：迁移永远触发不了")
	}
	sub := r.bus.Subscribe(0)
	as4 := r.orch.maybeMigrateGeneration(context.Background(), as1)
	if as4 == as1 {
		t.Fatal("指纹变化后应在轮次边界换代际")
	}
	if as4.Fingerprint != want {
		t.Fatalf("新句柄指纹 = %q，期望 %q", as4.Fingerprint, want)
	}
	if as4.client == as1.client {
		t.Fatal("新代际必须落在**另一个**连接上（能力集是进程级的，共用连接等于把两套能力合进一个进程）")
	}
	if n := len(r.orch.conns); n != 2 {
		t.Fatalf("迁移后应有两个代际的连接（旧的保留给其它会话/可回退），got %d", n)
	}
	// 活跃集合里换的是**新**句柄；旧句柄作废（拿到旧指针的路径会走重建而不是继续用）。
	if cur := r.orch.conversation(convID); cur != as4 {
		t.Fatal("活跃集合应指向新句柄")
	}
	as1.mu.Lock()
	deadOld := as1.dead
	as1.mu.Unlock()
	if !deadOld {
		t.Fatal("旧句柄应被标废")
	}
	// 落库：库里必须记新的代际（否则重启后又挂回旧代际）。
	row, ok, err := r.svc.st.GetConversation(context.Background(), convID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if row.CapabilityFingerprint != want {
		t.Fatalf("库里指纹 = %q，期望 %q", row.CapabilityFingerprint, want)
	}
	if row.CapabilityJSON != MarshalCapabilitySelection(sel) {
		t.Fatalf("迁移不得改动选定明细（变的只是它解析出的代际）: %q", row.CapabilityJSON)
	}
	// 事件：迁移必须可被看到（前端据此提示"能力已更新"）。
	var evt *eventbus.Event
	deadline := 20
	for i := 0; i < deadline; i++ {
		select {
		case e := <-sub.C:
			if e.Type == BusSessionResumed && e.AggregID == convID {
				evt = &e
			}
		default:
			i = deadline
		}
		if evt != nil {
			break
		}
	}
	r.bus.Unsubscribe(sub)
	if evt == nil {
		t.Fatal("迁移应发布 session.resumed（reason=generation）")
	}
	payload, _ := evt.Payload.(map[string]any)
	if got := payload["reason"]; got != "generation" {
		t.Fatalf("reason = %v，期望 generation（与「重启后原生续接」必须可区分）", got)
	}
	if got := payload["capability_fingerprint"]; got != want {
		t.Fatalf("事件应带上换到的那一代: %v", got)
	}
	// 注入：新代际的 session/new 必须收到**新**的 fs 定义、且只有 fs（白名单）。
	news := log()
	if len(news) < 2 {
		t.Fatalf("应有两次 session/new（建会话 + 迁移），got %d", len(news))
	}
	last := news[len(news)-1]
	servers, _ := last["mcpServers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("新代际的注入集应只有白名单里的 fs，got %v", last["mcpServers"])
	}
	body, _ := json.Marshal(servers[0])
	if !strings.Contains(string(body), "v2") {
		t.Fatalf("新代际必须按**新**定义下发（v2），got %s", body)
	}
	if strings.Contains(string(body), "git") || strings.Contains(string(body), "extra") {
		t.Fatalf("注入集漏了白名单过滤: %s", body)
	}
}

// TestGenerationMigrationDefersWhileBusy 忙则顺延（纪律②）：在途轮次与未应答的挂起项
// 都不切；它们结束后**下一轮**自然切上。
func TestGenerationMigrationDefersWhileBusy(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"})
	convID, as1 := r.startedConversation(t, t.TempDir(), &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})

	// 让指纹变化。
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v2"}})

	// ②-a 在途轮次：不切。
	as1.mu.Lock()
	as1.busy = true
	as1.mu.Unlock()
	if got := r.orch.maybeMigrateGeneration(context.Background(), as1); got != as1 {
		t.Fatal("有在途轮次时不得换代际（绝不打断）")
	}
	// 轮次结束 → 下一个边界切上（"下一轮生效"不是"永远不生效"）。
	as1.mu.Lock()
	as1.busy = false
	as1.mu.Unlock()
	as2 := r.orch.maybeMigrateGeneration(context.Background(), as1)
	if as2 == as1 {
		t.Fatal("空闲后应在下一个轮次边界完成迁移")
	}

	// ②-b 未应答的权限/提问：同样顺延（别把用户正盯着的确认卡换到另一个进程去）。
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v3"}})
	plan := r.orch.planGenerationSwitch(as2, r.svc.conversationRec(convID))
	if plan.Action != genActionSwitch {
		t.Fatalf("空闲且指纹变化时应可切，got %s（%s）", plan.Action, plan.Reason)
	}
	r.orch.elic.pending["pending-1"] = &pendingElicitation{conversationID: convID}
	plan = r.orch.planGenerationSwitch(as2, r.svc.conversationRec(convID))
	if plan.Action != genActionDefer {
		t.Fatalf("有挂起提问时应顺延，got %s（%s）", plan.Action, plan.Reason)
	}
	if got := r.orch.maybeMigrateGeneration(context.Background(), as2); got != as2 {
		t.Fatal("有挂起提问时不得换代际")
	}
	delete(r.orch.elic.pending, "pending-1")
	if got := r.orch.maybeMigrateGeneration(context.Background(), as2); got == as2 {
		t.Fatal("挂起项收敛后应在下一个轮次边界完成迁移")
	}
}

// TestGenerationMigrationKeepsDefaultPath 缺省路径（未声明能力选择）不参与迁移，且
// 句柄/连接与引入代际之前完全一致（R11 的迁移侧推论）。
func TestGenerationMigrationKeepsDefaultPath(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"})
	convID, as1 := r.startedConversation(t, t.TempDir(), nil)
	if as1.Fingerprint != "" {
		t.Fatalf("未声明能力选择的会话指纹应为空，got %q", as1.Fingerprint)
	}
	// 定义变化 + 有声明会话才有的那些前提，对缺省会话一律不触发迁移。
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v2"}})
	plan := r.orch.planGenerationSwitch(as1, r.svc.conversationRec(convID))
	if plan.Action != genActionKeep {
		t.Fatalf("缺省路径不做代际迁移，got %s（%s）", plan.Action, plan.Reason)
	}
	if got := r.orch.maybeMigrateGeneration(context.Background(), as1); got != as1 {
		t.Fatal("缺省路径不得换代际")
	}
	if n := len(r.orch.conns); n != 1 {
		t.Fatalf("缺省路径应始终只有一个连接，got %d", n)
	}
}

// TestMigrationFallback 纪律④：迁移失败 → **回退旧代际继续跑**，不产生"会话不可用"。
//
// 构造失败的办法：把 memo 兜底关掉（`handoffMaxChars = 0`），于是新代际上既不能原生 resume
// （桩不声明 load/resume）、又不能建 fresh（无 memo 兜底）→ connectConversation 报
// resume_unavailable。此时会话必须继续挂在旧代际上，且**不向调用方报错**。
func TestMigrationFallback(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"})
	convID, as1 := r.startedConversation(t, t.TempDir(), &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	oldFP := as1.Fingerprint

	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v2"}})
	r.orch.handoffMaxChars = 0 // 关掉兜底：新代际上开不出会话来

	got := r.orch.maybeMigrateGeneration(context.Background(), as1)
	if got != as1 {
		t.Fatal("迁移失败必须回退旧句柄（可用性优先）")
	}
	if got.Fingerprint != oldFP {
		t.Fatalf("回退后指纹应保持旧值 %q，got %q", oldFP, got.Fingerprint)
	}
	row, ok, err := r.svc.st.GetConversation(context.Background(), convID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if row.CapabilityFingerprint != oldFP {
		t.Fatalf("迁移失败不得把库里指纹改成没挂上的那一代: %q", row.CapabilityFingerprint)
	}
	// 诊断留痕：失败这件事必须看得见（否则「能力没换过来」只能靠猜）。
	r.svc.diagMu.Lock()
	last, hasDiag := r.svc.lastErrs["a1"]
	r.svc.diagMu.Unlock()
	if !hasDiag {
		t.Fatal("迁移失败应落一条 agent 诊断（否则「能力没换过来」只能靠猜）")
	}
	if last.Code != "generation_switch_failed" {
		t.Fatalf("诊断码 = %q，期望 generation_switch_failed", last.Code)
	}
	if last.ConversationID != convID {
		t.Fatalf("诊断应指认会话: %+v", last)
	}
}

// TestEnsureConversationMigratesOnRebuild 进程死掉后重建 = 迁移（同一条路径的不同入口）。
//
// 这条路径不经过"活句柄"，而是 `ensureConversation` 重开会话——它必须按**当前定义**重算代际，
// 否则"改了定义 + 进程重启"这个最常见的组合永远吃不上新能力。
func TestEnsureConversationMigratesOnRebuild(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"})
	convID, as1 := r.startedConversation(t, t.TempDir(), &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	oldFP := as1.Fingerprint

	// 模拟进程死掉：句柄作废并摘除（与拆连路径的收尾一致）。
	r.orch.disconnectConversation(as1, true)

	r.registerMcp(t, capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo", Args: []string{"v2"}})
	agent, _ := r.svc.AgentByID("a1")
	want := r.svc.capabilityFingerprint(agent, "stub", ParseCapabilitySelection(r.svc.conversationRec(convID).CapabilityJSON))
	if want == oldFP {
		t.Fatal("改了定义就该算出新指纹")
	}
	as2, err := r.orch.ensureConversation(context.Background(), convID)
	if err != nil {
		t.Fatalf("重建: %v", err)
	}
	if as2.Fingerprint != want {
		t.Fatalf("重建应落在当前代际 %q，got %q", want, as2.Fingerprint)
	}
	if row, _, _ := r.svc.st.GetConversation(context.Background(), convID); row.CapabilityFingerprint != want {
		t.Fatalf("重建后库里指纹应更新为 %q，got %q", want, row.CapabilityFingerprint)
	}
}
