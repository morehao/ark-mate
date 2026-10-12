package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/mcp"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— M3-6/M3-7：轮次边界的代际迁移、缩权与代际池 ———
//
// `generation_test.go` 管的是**代际键与连接池的键面**（M1：键怎么算、怎么复用、怎么一次性关掉）；
// 本文件管的是**会话在轮次边界换不换代际、缩权要不要立刻生效、代际池满了怎么办**
// （M3-6 的四条纪律 + M3-7 的缩权与上限）。
//
// 判据全部落在**可观察的事实**上（句柄指针、`conns` 条数、库里的指纹、session/new 真的收到了
// 什么、事件里的 reason、错误的码与文案），而不是"决策函数返回了什么"——决策再对，句柄没换
// 也等于没迁；而且**取句柄一律走 `acquireSession`**（Prompt 与排队投递的真实入口）：
// 只测判定函数会漏掉"判定对但没接线"这一整类 bug。

// genRig 迁移用例的架子：stub runtime + sqlite 库 + 一个名为 a1 的 agent。
//
// 带库是有意的：**指纹换没换最终以库里的行为准**（重启后按它决定挂哪个代际），
// 只在内存里换等于下次重启又漂回去。
type genRig struct {
	svc    *Service
	orch   *Orchestrator
	bus    *eventbus.Bus
	runtme string // agent 声明的 runtime id（= 注册表里的 spec id）
}

func newGenRig(t *testing.T) *genRig { return newGenRigOn(t, "stub") }

// newGenRigOn 指定 spec/agent 的 runtime id。
//
// 用真 runtime id（如 `dsh`）+ 桩命令是有意的，但**光有真 id 不够**：原生权限实现挂在注册表的
// 第三张表上（ADR-2：`Spec` 只装纯数据、行为进执行表），所以自造注册表必须显式登记内置可执行半场
// （`runtime.RegisterBuiltinPorts`）——不登记的表现是"这个底座没有原生权限开关"，
// "收回权限 → 基础键变化"这条链路就静默走不通了。
func newGenRigOn(t *testing.T, runtimeID string) *genRig {
	t.Helper()
	// 底座是构建期常量：夹具只用这一个 spec，就得把它设成构建期选中的那个，
	// 否则 ResolveRuntime 一律报"未装配"（新语义下路由没有第二个输入了）。
	setBuildRuntime(t, runtimeID)
	spec := stubACPSpec(t)
	spec.ID = runtimeID
	spec.Name = runtimeID
	spec.McpTransports = []string{"stdio"} // 桩只承载 stdio（保守形态）
	reg := runtime.NewRegistry(spec)
	// 与产品装配同形：桩 Spec 保留自己的 Launch（桩命令），只把执行半场登记进来。
	runtime.RegisterBuiltinPorts(reg)
	bus := eventbus.New()
	svc := NewService(reg, bus)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc.EnableStore(st)
	// v13：没有可用 vault 时**拒绝**存密钥（fail-closed），所以凡是要建带密钥的供应商的夹具，
	// 都必须给一个真实（且私有）的 mate home——正是生产形态。目录在 t.TempDir 下，绝不碰用户环境。
	svc.agentDir = filepath.Join(t.TempDir(), "agents")
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", State: AgentStateStopped}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return &genRig{svc: svc, orch: orch, bus: bus, runtme: runtimeID}
}

// registerMcp 往注册表里放一个 stdio MCP（模拟 `mcp/*.json` 装载/改动）。
func (r *genRig) registerMcp(t *testing.T, m capability.McpServer) {
	t.Helper()
	if err := r.svc.caps.RegisterMcpServer(&m); err != nil {
		t.Fatalf("注册 MCP %s: %v", m.ID, err)
	}
}

// fsServer 造一个 fs 定义（args 就是"版本"，用来制造指纹变化）。
func fsServer(args ...string) capability.McpServer {
	return capability.McpServer{ID: "fs", Name: "fs", Transport: capability.TransportStdio,
		Command: "/bin/echo", Args: args}
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

// start 建一条钉住 sel 的 a1 会话。
func (r *genRig) start(t *testing.T, sel *CapabilitySelection) (string, *ActiveSession) {
	t.Helper()
	return r.startAs(t, "a1", sel)
}

// startAs 给指定 agent 建一条会话，返回 id 与当时的句柄。
func (r *genRig) startAs(t *testing.T, agentID string, sel *CapabilitySelection) (string, *ActiveSession) {
	t.Helper()
	agent, _ := r.svc.AgentByID(agentID)
	res, err := r.orch.startSessionOn(context.Background(), agent,
		StartOptions{Cwd: t.TempDir(), Selection: sel})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	as := r.orch.session(res.SessionID)
	if as == nil {
		t.Fatal("建会话后应有活句柄")
	}
	return res.SessionID, as
}

// boundary 走**真实入口**取这一轮的句柄（Prompt / 排队投递都经它）。
func (r *genRig) boundary(t *testing.T, convID string) *ActiveSession {
	t.Helper()
	as, err := r.orch.acquireSession(context.Background(), convID)
	if err != nil {
		t.Fatalf("取句柄: %v", err)
	}
	return as
}

// row 读库里那三个 M3 列里的指纹与选定明细。
func (r *genRig) row(t *testing.T, convID string) (fingerprint, capJSON string) {
	t.Helper()
	row, ok, err := r.svc.st.GetSession(context.Background(), convID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	return row.CapabilityFingerprint, row.CapabilityJSON
}

// waitConns 轮询等连接数落到 want（拆除是异步的：watchConn 观察到 Done 之后才 dropConn 摘除）。
func (r *genRig) waitConns(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	n := r.conns()
	for n != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		n = r.conns()
	}
	return n
}

func (r *genRig) conns() int {
	r.orch.mu.Lock()
	defer r.orch.mu.Unlock()
	return len(r.orch.conns)
}

// TestGenerationMigration 轮次边界的迁移：指纹真变了才切、只按白名单内的变化切，
// 换句柄/换连接/落库/事件/注入五件事一起发生，且旧代际（已无人引用）当场回收。
func TestGenerationMigration(t *testing.T) {
	r := newGenRig(t)
	log := sessionNewLog(t)
	r.registerMcp(t, fsServer("v1"))
	r.registerMcp(t, capability.McpServer{ID: "git", Name: "git", Transport: capability.TransportStdio, Command: "/bin/echo"})

	// 钉住「只要 fs」：git 在注册表里、也在默认集里，但不在这条会话的白名单里。
	convID, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	if as1.Fingerprint == "" {
		t.Fatal("钉住选定的会话必须有指纹")
	}

	// ① 同一套定义再取句柄 → **同一个**句柄（绝不每轮切）。这是最常见的一轮，
	//    它必须零开销：连"要不要切"的判断都不该换句柄。
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("指纹未变时不得换代际")
	}

	// ① 的强化：加一个**不在白名单里**的 MCP → 指纹不变 → 仍不切。
	//    "只纳入真正会生效的东西"这条纪律要是破了，用户加一个跟自己无关的 server
	//    就会把在跑的会话换一次代际（白丢上下文）。
	r.registerMcp(t, capability.McpServer{ID: "extra", Name: "extra", Transport: capability.TransportStdio, Command: "/bin/echo"})
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("白名单外的定义变化不得换代际（它对这个会话的能力集没有任何影响）")
	}

	// ② 真的改了白名单内的定义（fs 的 args）→ 指纹变了 → 本轮切。
	r.registerMcp(t, fsServer("v2"))
	agent, _ := r.svc.AgentByID("a1")
	sel := ParseCapabilitySelection(r.svc.sessionRec(convID).CapabilityJSON)
	want := r.svc.capabilityFingerprint(agent, r.runtme, sel)
	if want == as1.Fingerprint {
		t.Fatal("改了白名单内定义却没换指纹：迁移永远触发不了")
	}
	sub := r.bus.Subscribe(0)
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("指纹变化后应在轮次边界换代际")
	}
	if as2.Fingerprint != want {
		t.Fatalf("新句柄指纹 = %q，期望 %q", as2.Fingerprint, want)
	}
	if as2.client == as1.client {
		t.Fatal("新代际必须落在**另一个**连接上（能力集是进程级的，共用连接等于把两套能力合进一个进程）")
	}
	// 旧代际此刻已无人引用 → 当场回收（不等空闲阈值）：它代表的是一套已经过时的能力集。
	if n := r.conns(); n != 1 {
		t.Fatalf("迁移后应只剩新代际（旧代际无人引用即回收），got %d", n)
	}
	if cur := r.orch.session(convID); cur != as2 {
		t.Fatal("活跃集合应指向新句柄")
	}
	as1.mu.Lock()
	deadOld := as1.dead
	as1.mu.Unlock()
	if !deadOld {
		t.Fatal("旧句柄应被标废")
	}
	// 落库：库里必须记新的代际（否则重启后又挂回旧代际）。
	fp, capJSON := r.row(t, convID)
	if fp != want {
		t.Fatalf("库里指纹 = %q，期望 %q", fp, want)
	}
	if capJSON != MarshalCapabilitySelection(sel) {
		t.Fatalf("迁移不得改动选定明细（变的只是它解析出的代际）: %q", capJSON)
	}
	// 事件：迁移必须可被看到（前端据此提示"能力已更新"）。
	evt := drainOne(sub, convID, BusSessionResumed)
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
	// 注入：新代际的 session/new 必须收到**新**定义、且只有 fs（白名单）。
	news := log()
	if len(news) < 2 {
		t.Fatalf("应有两次 session/new（建会话 + 迁移），got %d", len(news))
	}
	last := news[len(news)-1]
	servers, _ := last["mcpServers"].([]any)
	// M7-3 之后注入集里恒有一条平台网关条目（条目恒定、与"有没有远程 server"无关），
	// 这里断言的是**白名单过滤**，故只数非网关的那几条。
	direct := make([]any, 0, len(servers))
	for _, s := range servers {
		if b, err := json.Marshal(s); err == nil && strings.Contains(string(b), mcp.GatewayServerName) {
			continue
		}
		direct = append(direct, s)
	}
	if len(direct) != 1 {
		t.Fatalf("新代际的注入集应只有白名单里的 fs（网关条目另计），got %v", last["mcpServers"])
	}
	body, _ := json.Marshal(direct[0])
	if !strings.Contains(string(body), "v2") {
		t.Fatalf("新代际必须按**新**定义下发（v2），got %s", body)
	}
	if strings.Contains(string(body), "git") || strings.Contains(string(body), "extra") {
		t.Fatalf("注入集漏了白名单过滤: %s", body)
	}
}

// drainOne 取该聚合上第一条指定类型的事件（最多等 2s；够用且不 flaky）。
func drainOne(sub *eventbus.Subscription, aggID, typ string) *eventbus.Event {
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Type == typ && e.AggregID == aggID {
				return &e
			}
		case <-timeout:
			return nil
		}
	}
}

// TestGenerationMigrationDefersWhileBusy 忙则顺延（纪律②）：在途轮次与未应答的挂起项
// 都不切；它们结束后**下一轮**自然切上。
func TestGenerationMigrationDefersWhileBusy(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	convID, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})

	// 让代际键变化。
	r.registerMcp(t, fsServer("v2"))

	// ②-a 在途轮次：不切。
	as1.mu.Lock()
	as1.busy = true
	as1.mu.Unlock()
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("有在途轮次时不得换代际（绝不打断）")
	}
	// 轮次结束 → 下一个边界切上（"下一轮生效"不是"永远不生效"）。
	as1.mu.Lock()
	as1.busy = false
	as1.mu.Unlock()
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("空闲后应在下一个轮次边界完成迁移")
	}

	// ②-b 未应答的权限/提问：同样顺延（别把用户正盯着的确认卡换到另一个进程去）。
	r.registerMcp(t, fsServer("v3"))
	if plan := r.orch.planGenerationSwitch(as2, r.svc.sessionRec(convID)); plan.Action != genActionSwitch {
		t.Fatalf("空闲且键变化时应可切，got %s（%s）", plan.Action, plan.Reason)
	}
	r.orch.elic.pending["pending-1"] = &pendingElicitation{sessionID: convID}
	if plan := r.orch.planGenerationSwitch(as2, r.svc.sessionRec(convID)); plan.Action != genActionDefer {
		t.Fatalf("有挂起提问时应顺延，got %s（%s）", plan.Action, plan.Reason)
	}
	if got := r.boundary(t, convID); got != as2 {
		t.Fatal("有挂起提问时不得换代际")
	}
	delete(r.orch.elic.pending, "pending-1")
	if got := r.boundary(t, convID); got == as2 {
		t.Fatal("挂起项收敛后应在下一个轮次边界完成迁移")
	}
}

// TestGenerationMigrationKeepsDefaultPath 缺省路径（未声明能力选择）不因**能力定义**变化
// 而迁移，句柄/连接与引入代际之前一致（R11 的迁移侧推论）。
func TestGenerationMigrationKeepsDefaultPath(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	convID, as1 := r.start(t, nil)
	if as1.Fingerprint != "" {
		t.Fatalf("未声明能力选择的会话指纹应为空，got %q", as1.Fingerprint)
	}
	r.registerMcp(t, fsServer("v2"))
	if plan := r.orch.planGenerationSwitch(as1, r.svc.sessionRec(convID)); plan.Action != genActionKeep {
		t.Fatalf("缺省路径不因定义变化迁移，got %s（%s）", plan.Action, plan.Reason)
	}
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("缺省路径不得换代际")
	}
	if n := r.conns(); n != 1 {
		t.Fatalf("缺省路径应始终只有一个连接，got %d", n)
	}
}

// TestMigrationFallback 纪律④：**能力集内容**变化导致的迁移失败 → 回退旧代际继续跑，
// 不产生"会话不可用"。
//
// 构造失败的办法：把 memo 兜底关掉（`handoffMaxChars = 0`），于是新代际上既不能原生 resume
// （桩不声明 load/resume）、又不能建 fresh（无 memo 兜底）→ connectSession 报
// resume_unavailable。此时会话必须继续挂在旧代际上，且**不向调用方报错**。
func TestMigrationFallback(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	convID, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	oldFP := as1.Fingerprint

	r.registerMcp(t, fsServer("v2"))
	r.orch.handoffMaxChars = 0 // 关掉兜底：新代际上开不出会话来

	got := r.boundary(t, convID)
	if got != as1 {
		t.Fatal("迁移失败必须回退旧句柄（可用性优先）")
	}
	if got.Fingerprint != oldFP {
		t.Fatalf("回退后指纹应保持旧值 %q，got %q", oldFP, got.Fingerprint)
	}
	if fp, _ := r.row(t, convID); fp != oldFP {
		t.Fatalf("迁移失败不得把库里指纹改成没挂上的那一代: %q", fp)
	}
	// 诊断留痕：失败这件事必须看得见（否则「能力没换过来」只能靠猜）。
	r.svc.diagMu.Lock()
	last, hasDiag := r.svc.lastErrs["a1"]
	r.svc.diagMu.Unlock()
	if !hasDiag {
		t.Fatal("迁移失败应落一条 agent 诊断")
	}
	if last.Code != "generation_switch_failed" {
		t.Fatalf("诊断码 = %q，期望 generation_switch_failed", last.Code)
	}
	if last.SessionID != convID {
		t.Fatalf("诊断应指认会话: %+v", last)
	}
}

// TestPermissionWithdrawalDoesNotFallBack 缩权立刻生效（M3-7 / ADR-13 的不对称规则）：
// 权限面被收回（基础键变）而新代际建不起来时，**不许回退**——旧进程代表的正是那套已经被
// 收回的权限面，宁可这一轮带码拒发。
func TestPermissionWithdrawalDoesNotFallBack(t *testing.T) {
	// 真 runtime id（dsh）+ 桩命令：原生权限映射按 runtime id 分派，
	// 只有真 id 才产得出 `DSH_PERMISSION_MODE=`，"收回权限 → 基础键变化"这条链路才走得到。
	r := newGenRigOn(t, "dsh")
	r.registerMcp(t, fsServer("v1"))
	r.svc.agents["a1"].PermissionPolicy = &PermissionPolicy{NativeMode: NativeModeWorkspaceWrite}
	convID, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	oldRC := r.orch.connOfClient(as1.client)
	if oldRC == nil {
		t.Fatal("应能反查到连接")
	}
	if !strings.Contains(oldRC.baseKey, "workspace-write") {
		t.Fatalf("初始基础键应含 workspace-write: %q", oldRC.baseKey)
	}

	// 收回权限：workspace-write → read-only。**定义一个字都没动**，变的只是权限面。
	r.svc.agents["a1"].PermissionPolicy = &PermissionPolicy{NativeMode: NativeModeReadOnly}
	plan := r.orch.planGenerationSwitch(as1, r.svc.sessionRec(convID))
	if plan.Action != genActionSwitch || !plan.PermChanged {
		t.Fatalf("收回权限应触发迁移且标为缩权面：%+v", plan)
	}
	if plan.FingerprintChanged {
		t.Fatalf("定义没动，指纹不该变：%+v", plan)
	}

	r.orch.handoffMaxChars = 0 // 新代际建不起来
	_, err := r.orch.acquireSession(context.Background(), convID)
	if err == nil {
		t.Fatal("缩权面变化 + 新代际建不起来时必须拒发这一轮（不许回退到被收回的权限面）")
	}
	ae, ok := acl.As(err)
	if !ok || ae.Code != "generation_switch_required" {
		t.Fatalf("错误应带码 generation_switch_required，got %v", err)
	}
	// 文案要说明"不会用已经收回的权限面继续跑"（用户得知道为什么这一轮没发出去、该怎么办）。
	if !strings.Contains(err.Error(), "已经收回") {
		t.Fatalf("文案应说明不会用已收回的权限面继续跑，got %v", err)
	}
	// 会话本身没被弄坏：句柄还在、配置修好后照常可用。
	if cur := r.orch.session(convID); cur != as1 {
		t.Fatal("拒发不得把会话句柄弄丢（修好配置后还要能继续）")
	}
	r.orch.handoffMaxChars = DefaultHandoffMaxChars
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("权限面修好后应能迁到新代际")
	}
	if rc := r.orch.connOfClient(as2.client); rc == nil || !strings.Contains(rc.baseKey, "read-only") {
		t.Fatal("新代际必须落在收窄后的权限键上")
	}
}

// TestGenerationPool 代际池（M3-7）：复用 / 显式淘汰空闲代际 / 上限带码拒绝 /
// 已达上限时仍可复用现有代际 / 0 = 不限制。
func TestGenerationPool(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	r.registerMcp(t, capability.McpServer{ID: "git", Name: "git", Transport: capability.TransportStdio, Command: "/bin/echo"})
	ctx := context.Background()
	agent, _ := r.svc.AgentByID("a1")
	spec, _ := r.orch.reg.Get(r.runtme)

	r.orch.SetGenerationLimit(2)
	// 一条**无人引用**的代际（默认指纹，没有会话挂它）+ 一条**有会话引用**的代际（选定不同 → 指纹不同）。
	idle, err := r.orch.acquire(ctx, agent, spec, "")
	if err != nil {
		t.Fatalf("acquire(缺省): %v", err)
	}
	_, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	if as1.client == idle.client {
		t.Fatal("不同能力集必须各起一个代际")
	}
	if n := r.conns(); n != 2 {
		t.Fatalf("应有 2 个代际，got %d", n)
	}
	// 复用：同一条会话再取一次不新增代际。
	if got := r.boundary(t, as1.ID); got != as1 {
		t.Fatal("同指纹应复用同一代际")
	}
	if n := r.conns(); n != 2 {
		t.Fatalf("复用不该新增代际，got %d", n)
	}
	// 显式淘汰空闲代际：池子满了，但其中一条无人引用 → 当场淘汰它腾出位置（不报错）。
	if _, err := r.orch.acquire(ctx, agent, spec, "fp-third"); err != nil {
		t.Fatalf("有无人引用的代际可淘汰时应允许新建: %v", err)
	}
	if n := r.conns(); n != 2 {
		t.Fatalf("淘汰空闲代际后池子应仍为 2，got %d", n)
	}
	if r.orch.connOfClient(idle.client) != nil {
		t.Fatal("无人引用的那条代际应已被淘汰")
	}
	// 换一条有会话引用的代际（git）：仍靠淘汰"fp-third"（此刻无人引用）腾位置。
	_, as2 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"git"})})
	if n := r.conns(); n != 2 {
		t.Fatalf("仍应为 2 个代际，got %d", n)
	}
	// 现在两条**都有会话引用** → 第三条带码拒绝，且绝不悄悄复用一个错的能力集。
	_, err = r.orch.acquire(ctx, agent, spec, "fp-fourth")
	if err == nil {
		t.Fatal("达上限且都有引用时应拒绝新建代际")
	}
	ae, ok := acl.As(err)
	if !ok || ae.Code != "generation_limit_reached" {
		t.Fatalf("错误应带码 generation_limit_reached，got %v", err)
	}
	// 拒绝文案必须给出路（用户看得懂"我该怎么办"）。
	for _, want := range []string{"上限", "MATE_GENERATION_LIMIT", "复用"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("拒绝文案缺 %q：%v", want, err)
		}
	}
	if n := r.conns(); n != 2 {
		t.Fatalf("拒绝时不得改动池子，got %d", n)
	}
	// 已达上限仍可复用现有代际（上限只管"新建"，不管"用"）。
	if _, err := r.orch.acquire(ctx, agent, spec, as1.Fingerprint); err != nil {
		t.Fatalf("已达上限时复用现有代际不该报错: %v", err)
	}
	if _, err := r.orch.acquire(ctx, agent, spec, as2.Fingerprint); err != nil {
		t.Fatalf("已达上限时复用现有代际不该报错: %v", err)
	}
	if n := r.conns(); n != 2 {
		t.Fatalf("复用不该新增代际，got %d", n)
	}
	// 0 = 不限制（显式关掉护栏）。
	r.orch.SetGenerationLimit(0)
	for _, fp := range []string{"fp-x", "fp-y", "fp-z"} {
		if _, err := r.orch.acquire(ctx, agent, spec, fp); err != nil {
			t.Fatalf("0 = 不限制时不该拒绝（fp=%s）: %v", fp, err)
		}
	}
}

// TestMigrationReclaimsSharedGenerationOnlyWhenUnreferenced 迁移后的立即回收**只看引用**：
// 还有别的会话挂在旧代际上就不动它（那些会话在自己的下一个轮次边界迁走），最后一个走了才收。
func TestMigrationReclaimsSharedGenerationOnlyWhenUnreferenced(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	sel := &CapabilitySelection{Mcps: ptrOf([]string{"fs"})}
	// 两条会话钉住**同一套**能力 → 共用同一个代际。
	c1, a1 := r.start(t, sel)
	c2, a2 := r.start(t, sel)
	if a1.client != a2.client {
		t.Fatal("同一套能力应共用代际")
	}
	oldClient := a1.client

	r.registerMcp(t, fsServer("v2"))
	_ = r.boundary(t, c1)
	if n := r.conns(); n != 2 {
		t.Fatalf("还有会话引用旧代际时不得回收它，got %d", n)
	}
	_ = r.boundary(t, c2)
	if n := r.conns(); n != 1 {
		t.Fatalf("最后一个会话迁走后应回收旧代际，got %d", n)
	}
	if r.orch.connOfClient(oldClient) != nil {
		t.Fatal("旧代际应已被回收")
	}
}

// TestEnsureSessionMigratesOnRebuild 进程死掉后重建 = 迁移（同一条路径的不同入口）。
//
// 这条路径不经过"活句柄"，而是 `ensureSession` 重开会话——它必须按**当前定义**重算代际，
// 否则"改了定义 + 进程重启"这个最常见的组合永远吃不上新能力。
func TestEnsureSessionMigratesOnRebuild(t *testing.T) {
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	convID, as1 := r.start(t, &CapabilitySelection{Mcps: ptrOf([]string{"fs"})})
	oldFP := as1.Fingerprint

	// 模拟进程死掉：句柄作废并摘除（与拆连路径的收尾一致）。
	r.orch.disconnectSession(as1, true)

	r.registerMcp(t, fsServer("v2"))
	agent, _ := r.svc.AgentByID("a1")
	sel := ParseCapabilitySelection(r.svc.sessionRec(convID).CapabilityJSON)
	want := r.svc.capabilityFingerprint(agent, r.runtme, sel)
	if want == oldFP {
		t.Fatal("改了定义就该算出新指纹")
	}
	as2, err := r.orch.ensureSession(context.Background(), convID)
	if err != nil {
		t.Fatalf("重建: %v", err)
	}
	if as2.Fingerprint != want {
		t.Fatalf("重建应落在当前代际 %q，got %q", want, as2.Fingerprint)
	}
	if fp, _ := r.row(t, convID); fp != want {
		t.Fatalf("重建后库里指纹应更新为 %q，got %q", want, fp)
	}
}

// TestStopAgentClosesDriftedGenerations 停 agent 要连**旧权限键下**的代际一起关。
//
// 场景：会话在"无原生权限配置"时建起来，之后权限被收窄（基础键变了，代际迁移需要轮次边界，
// 此时还没发生）→ 这时停 agent。只按"当前配置的基础键"扫会漏掉旧键下的那条代际，
// 表现就是"agent 已停、旧权限面的进程还在跑，直到空闲回收"。判据只看会话归属就不受漂移影响。
// 同时验证：与别的 agent 共用的代际**不**因为停 A 而被关。
func TestStopAgentClosesDriftedGenerations(t *testing.T) {
	r := newGenRigOn(t, "dsh")
	ctx := context.Background()
	r.registerMcp(t, fsServer("v1"))
	sel := &CapabilitySelection{Mcps: ptrOf([]string{"fs"})}

	// a1 的会话建在"无原生权限配置"的基础键上（此刻没有别的 agent 参与）。
	convID, as1 := r.start(t, sel)
	oldClient := as1.client
	if rc := r.orch.connOfClient(oldClient); rc == nil || !strings.Contains(rc.baseKey, "dsh") {
		t.Fatalf("初始连接基础键异常: %+v", rc)
	}

	// 权限被收窄：当前配置的基础键变了，但会话仍挂在旧键的代际上（迁移要等下一个轮次边界）。
	r.svc.agents["a1"].PermissionPolicy = &PermissionPolicy{NativeMode: NativeModeReadOnly}
	curBase := r.orch.baseKeyFor(r.svc.agents["a1"], mustSpec(t, r, r.runtme))
	if rc := r.orch.connOfClient(oldClient); rc == nil || rc.baseKey == curBase {
		t.Fatal("权限收窄后基础键应当已变（否则这个用例根本没测到漂移）")
	}
	if n := len(r.orch.generationKeysFor(curBase)); n != 0 {
		t.Fatalf("新基础键下此刻不该有代际（迁移还没发生），got %d", n)
	}

	// 停 agent：旧键下的那条代际必须一起关（它装的仍是 a1 的会话）。
	if err := r.svc.StopAgent(ctx, "a1"); err != nil {
		t.Fatalf("停 agent: %v", err)
	}
	// 拆除是异步收尾（watchConn → dropConn），轮询等它落到 0（沿用既有停 agent 用例的口径）。
	if n := r.waitConns(t, 0); n != 0 {
		t.Fatalf("停 agent 后不该留下任何属于它的代际，got %d", n)
	}
	if r.orch.session(convID) != nil {
		t.Fatal("停 agent 后句柄应被摘除（会话记录保留，可续聊）")
	}
	if rec := r.svc.sessionRec(convID); rec == nil || rec.ID != convID {
		t.Fatal("停 agent 不得删会话记录（可续聊性只看会话是否存在）")
	}
}

// mustSpec 取注册表里的 spec（测试辅助）。
func mustSpec(t *testing.T, r *genRig, id string) runtime.Spec {
	t.Helper()
	spec, ok := r.orch.reg.Get(id)
	if !ok {
		t.Fatalf("runtime 未注册: %s", id)
	}
	return spec
}

// TestStopAgentKeepsSharedGenerations 停 A 不得关掉 A、B **共用**的代际（那是 B 正在用的进程）。
//
// 与上一个用例合起来构成本次改动的边界：补扫只认「这条代际上只有本 agent 的会话」。
func TestStopAgentKeepsSharedGenerations(t *testing.T) {
	r := newGenRigOn(t, "dsh")
	ctx := context.Background()
	r.svc.agents["a2"] = &AgentRec{ID: "a2", Name: "A2", State: AgentStateStopped}
	// M7-3 之后"两个**不同** agent 共用代际"只在没有 MCP 面时成立：网关条目带着 agent scope
	// （args 里的 `--agent` 不同 ⇒ 指纹不同），所以只要声明了 MCP 面，两个 agent 就各占一个代际。
	// 这是**保守方向**的代价：宁可多起一个进程，也不能让底座在一个进程里混用两个 agent 的工具面
	// （底座按进程缓存工具面与否尚无定论，见设计 §10.6-⑤）。代价记在计划/设计文档里。
	// 本用例要验的是"停 A 不得关掉 B 在用的代际"，故两侧都显式关掉 MCP 面（`mcps: []`）→ 指纹为空、
	// 二者逐字同键。
	denyAll := ptrOf([]string{})
	r.svc.agents["a1"].Mcps = denyAll
	r.svc.agents["a2"].Mcps = denyAll

	_, a1 := r.startAs(t, "a1", nil)
	_, a2 := r.startAs(t, "a2", nil)
	if a1.Fingerprint != "" || a2.Fingerprint != "" {
		t.Fatalf("显式关闭 MCP 面的缺省路径指纹应为空: %q / %q", a1.Fingerprint, a2.Fingerprint)
	}
	if a1.client != a2.client {
		t.Fatal("同配置（含同一 MCP 面）的两个 agent 应共用代际")
	}
	// a1 的权限被收窄（漂移），随后停 a1：这条代际上还有 a2 的会话 → 不能关。
	r.svc.agents["a1"].PermissionPolicy = &PermissionPolicy{NativeMode: NativeModeReadOnly}
	if err := r.svc.StopAgent(ctx, "a1"); err != nil {
		t.Fatalf("停 a1: %v", err)
	}
	if n := r.conns(); n != 1 {
		t.Fatalf("共用代际不该因为停 a1 而被关，got %d", n)
	}
	if got := r.orch.session(a2.ID); got != a2 {
		t.Fatal("a2 的会话必须原样可用")
	}
	if r.orch.session(a1.ID) != nil {
		t.Fatal("a1 的会话句柄应被摘除")
	}
}

// TestStopAgentClosesWarmGenerationUnderDriftedKey 停 agent 也要收掉**它创建的暖进程**
// （StartAgent 建、没有任何会话引用）。权限漂移后暖进程落在旧基础键下，前两类扫描都够不着它。
func TestStopAgentClosesWarmGenerationUnderDriftedKey(t *testing.T) {
	r := newGenRigOn(t, "dsh")
	ctx := context.Background()
	agent, _ := r.svc.AgentByID("a1")
	spec := mustSpec(t, r, r.runtme)

	// 暖进程：直接 acquire（不经会话，故无人引用）。
	warm, err := r.orch.acquire(ctx, agent, spec, "")
	if err != nil {
		t.Fatalf("暖启动: %v", err)
	}
	if n := r.conns(); n != 1 {
		t.Fatalf("应有 1 个暖代际，got %d", n)
	}
	// 权限漂移：当前基础键变了，暖进程仍在旧键下。
	r.svc.agents["a1"].PermissionPolicy = &PermissionPolicy{NativeMode: NativeModeReadOnly}
	if rc := r.orch.connOfClient(warm.client); rc == nil ||
		rc.baseKey == r.orch.baseKeyFor(r.svc.agents["a1"], spec) {
		t.Fatal("暖代际应留在旧基础键下（否则这个用例没测到漂移）")
	}
	if err := r.svc.StopAgent(ctx, "a1"); err != nil {
		t.Fatalf("停 agent: %v", err)
	}
	if n := r.waitConns(t, 0); n != 0 {
		t.Fatalf("停 agent 应连旧权限键下的暖进程一起收掉，got %d", n)
	}
}
