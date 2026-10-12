package manager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// connCount 读连接池条数（**持锁**读）。
//
// 为什么要一个访问器而不是直接 `len(o.conns)`：拆除是异步的（见 waitConnCount），
// 断言前后随时可能有 goroutine 在改这张表——裸读在 `-race` 下就是数据竞争，而本轮把
// 断言从"看一眼"改成了"等到满足"，窗口比原来大得多，必须持锁。
func connCount(o *Orchestrator) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.conns)
}

// waitConnCount 等连接池到达期望条数（上限 2s），超时才判失败。
//
// 为什么要等（T12 的根因，2026-09-29）：`dropConn` 由 `watchConn` 在 `client.Done()` 之后
// **异步**执行，而 `stopAgent` 等的正是同一个 Done——两者被同一件事唤醒，谁先跑完没有保证。
// 生产路径**刻意**容忍这个窗口：`dropConn` 用 `ownerBefore`（`o.conns[key] == rc`）判定所有权，
// 所以旧连接的晚收尾不会删掉/覆盖新连接；`acquire` 也先用非阻塞 `<-client.Done()` 验活，
// 撞上"已断开但还没摘表"的条目会落到重拨。因此"停 agent 返回时表已空"**不是** API 保证，
// 断言它就会在整包负载下随机红（历史记录见计划 §13 T12）。
func waitConnCount(t *testing.T, r *rig, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n := connCount(r.Orch); n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等连接池变成 %d 条超时，当前 %d 条", want, connCount(r.Orch))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestGenerationKeyZeroRegression R11：**指纹为空时键必须与今天逐字相同**。
//
// 这是代际机制唯一的零回归风险点：connKey 加一个维度后，未声明能力选择的会话产生的键若变了，
// 同配置的两个会话就会各起一个进程（"配置相同共享暖进程"这条既有规则被静默破坏，代价是进程数
// 与内存翻倍，而且没人会立刻发现）。
func TestGenerationKeyZeroRegression(t *testing.T) {
	base := connKey("opencode", []string{"A=1"}, []string{"B=2"})
	if got := generationKey(base, "", ""); got != base {
		t.Fatalf("空指纹必须原样返回基础键（R11）：\n%s\n%s", base, got)
	}
	// 非空指纹必须改键（否则两套不同的能力会共用一个进程）。
	if got := generationKey(base, "abc123", ""); got == base {
		t.Fatal("非空指纹必须产生不同的键")
	}
	// 同指纹稳定、不同指纹不同。
	if generationKey(base, "abc123", "") != generationKey(base, "abc123", "") {
		t.Fatal("同指纹应得同键")
	}
	if generationKey(base, "abc123", "") == generationKey(base, "def456", "") {
		t.Fatal("不同指纹应得不同键")
	}
}

// TestGenerationKeyNoPrefixAmbiguity 代际键不得与"另一个基础键"撞车。
//
// connKey 的组间分隔符就是 `\x00`，所以 `connKey("r", g1)` 是 `connKey("r", g1, g2)` 的字符串
// 前缀。若代际后缀是裸拼接，`("r",g1) + gen:X` 就可能与另一个基础键/代际键重合——基础键里
// 恰好出现 `gen:` 这种文本并非不可能（env 组是 `KEY=VALUE`，VALUE 由用户/provider 决定）。
// 长度前缀让基础键自我定界，从结构上排除这类碰撞。
func TestGenerationKeyNoPrefixAmbiguity(t *testing.T) {
	short := connKey("r", []string{"A=1"})
	long := connKey("r", []string{"A=1"}, []string{"gen:1"})

	// 短键 + 指纹 vs 长键本身：必须不同（否则"给短键指定了能力"会被当成"长键的缺省代际"，
	// 两套不同的能力共用一个进程）。
	if generationKey(short, "zz", "") == long {
		t.Fatalf("代际键与另一个基础键撞车：\nshort=%q\nlong =%q", generationKey(short, "zz", ""), long)
	}
	// 同指纹下两个不同基础键的键也必须不同。
	if generationKey(short, "zz", "") == generationKey(long, "zz", "") {
		t.Fatal("不同基础键的同指纹代际键不该相同")
	}
}

// TestGenerationKeyFindsAllGenerations 停机/停 agent 必须能找到**该配置的全部代际**。
func TestGenerationKeyFindsAllGenerations(t *testing.T) {
	base := connKey("stub", nil)
	o := &Orchestrator{conns: map[string]*runtimeConn{}}
	// 一个缺省代际 + 两个能力代际 + 一个别的配置（不该被匹配到）。
	o.conns[base] = &runtimeConn{key: base, baseKey: base}
	for _, fp := range []string{"fp1", "fp2"} {
		k := generationKey(base, fp, "")
		o.conns[k] = &runtimeConn{key: k, baseKey: base}
	}
	otherBase := connKey("stub", []string{"DSH_PERMISSION_MODE=read-only"})
	o.conns[otherBase] = &runtimeConn{key: otherBase, baseKey: otherBase}

	got := o.generationKeysFor(base)
	if len(got) != 3 {
		t.Fatalf("应找到 3 个代际键（含基础键），got %d: %v", len(got), got)
	}
	for _, k := range got {
		if k == otherBase {
			t.Fatalf("不该匹配到别的配置的键: %v", got)
		}
	}
}

// ackRig 带**真子进程** stub runtime 的架子（拨号/连接池类用例专用，见 stubACPSpec）。
func acpRig(t *testing.T) *rig {
	t.Helper()
	reg := runtime.NewRegistry(stubACPSpec(t))
	svc := NewService(reg, eventbus.New())
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", State: AgentStateStopped}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return &rig{Svc: svc, Orch: orch}
}

// TestWarmProcessSharingWithoutCapabilitySelection R11 的行为面（不只是键面）：
// 同一 runtime + 同权限 + 同 provider、**都不声明能力选择** → 两个会话共用同一个暖进程。
func TestWarmProcessSharingWithoutCapabilitySelection(t *testing.T) {
	r := acpRig(t)
	ctx := context.Background()
	cwd := t.TempDir()
	mk := func() string {
		t.Helper()
		resp, err := r.Svc.CreateSession(ctx, dtosession.SessionCreateReq{
			AgentID: "a1", Cwd: cwd,
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		return resp.ID
	}
	first, second := mk(), mk()

	if n := connCount(r.Orch); n != 1 {
		t.Fatalf("同配置的两个会话应共用 1 个暖进程，got %d", n)
	}
	c1 := r.Orch.session(first)
	c2 := r.Orch.session(second)
	if c1 == nil || c2 == nil {
		t.Fatal("两个会话都应活跃")
	}
	if c1.client != c2.client {
		t.Fatal("两个会话应挂在同一个连接上（缺省代际复用）")
	}
	if c1.Fingerprint != "" || c2.Fingerprint != "" {
		t.Fatalf("未声明能力选择时应是空指纹: %q / %q", c1.Fingerprint, c2.Fingerprint)
	}
	// 会话记录也要如实记着"没有能力选择"（恢复路径据此回到同一代际）。
	if rec := r.Svc.sessionRec(first); rec == nil || rec.Fingerprint != "" {
		t.Fatalf("会话记录的能力指纹应为空: %+v", rec)
	}
}

// TestGenerationPerFingerprint 不同能力指纹 → 不同进程（能力集是进程级的，混用等于把 A 会话的
// skill 投给 B 会话）。M1 还没有产生非空指纹的入口，所以这里直接在编排器层验证键与池行为：
// 两个指纹各拨一条连接，两条都必须各自独立存在。
func TestGenerationPerFingerprint(t *testing.T) {
	r := acpRig(t)
	ctx := context.Background()
	agent, _ := r.Svc.AgentByID("a1")
	spec, ok := r.Orch.reg.Get("stub")
	if !ok {
		t.Fatal("stub runtime 未注册")
	}
	for _, fp := range []string{"", "fp-a", "fp-b"} {
		if _, err := r.Orch.acquire(ctx, agent, spec, fp); err != nil {
			t.Fatalf("acquire(fp=%q): %v", fp, err)
		}
	}
	// 三个不同指纹 → 三条独立连接（含缺省代际）。
	if n := connCount(r.Orch); n != 3 {
		t.Fatalf("三个不同指纹应各有一条连接，got %d", n)
	}
	// 重复取同一个指纹必须复用，不新增。
	if _, err := r.Orch.acquire(ctx, agent, spec, "fp-a"); err != nil {
		t.Fatal(err)
	}
	if n := connCount(r.Orch); n != 3 {
		t.Fatalf("同指纹应复用连接，got %d 条", n)
	}
	// 全部代际都在同一个基础键下（停机时一次性关掉）。
	base := connKey(spec.ID, r.Svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy), r.Svc.providerEnvFor(spec.ID, agent.Provider))
	if n := len(r.Orch.generationKeysFor(base)); n != 3 {
		t.Fatalf("三代际应同属一个基础键: %d", n)
	}
	// 显式停 agent：三条连接全关（不是只关查表查到的第一个）。
	if err := r.Svc.StopAgent(ctx, "a1"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	// 等**最终**一致：连接已被关掉（Close + Done）是同步保证的；表里那条登记由
	// watchConn→dropConn 异步摘除（见 waitConnCount 的注释）。
	waitConnCount(t, r, 0)
}

// TestStopAgentBusyRejectsAcrossGenerations 任一代际上有在途 run → 整体拒绝
// （显式停机不该把跑着的一轮打断，也不该"关一半留一半"）。
func TestStopAgentBusyRejectsAcrossGenerations(t *testing.T) {
	r := acpRig(t)
	ctx := context.Background()
	agent, _ := r.Svc.AgentByID("a1")
	spec, _ := r.Orch.reg.Get("stub")
	idle, err := r.Orch.acquire(ctx, agent, spec, "")
	if err != nil {
		t.Fatal(err)
	}
	busy, err := r.Orch.acquire(ctx, agent, spec, "fp-busy")
	if err != nil {
		t.Fatal(err)
	}
	r.Orch.markBusy(busy.client, "cv-x") // 只有"忙"的那一代上有在途
	defer r.Orch.unmarkBusy(busy.client, "cv-x")

	err = r.Svc.StopAgent(ctx, "a1")
	if err == nil {
		t.Fatal("有在途 run 时应拒绝停机")
	}
	if !strings.Contains(err.Error(), "在途") {
		t.Fatalf("错误文案应说明原因: %v", err)
	}
	// 两条连接都必须还在（拒绝 = 什么都不做，不是"关掉空闲那条"）。
	if n := connCount(r.Orch); n != 2 {
		t.Fatalf("拒绝停机时不该关掉任何一代，剩 %d 条", n)
	}
	_ = idle
}

// TestAdaptersDeclareProcessScope M1-2 的验收面落在 archtest（第 10 条守卫），这里补一条
// 就近断言：两个内置 adapter 都声明 process 且未实测（P-2 未出结论前不放宽）。
func TestAdaptersDeclareProcessScope(t *testing.T) {
	for _, sp := range runtime.AllBuiltinRegistry().List() {
		if sp.CapabilityScope != runtime.CapabilityScopeProcess {
			t.Fatalf("%s 应声明 process：%q", sp.ID, sp.CapabilityScope)
		}
		if sp.Tested {
			t.Fatalf("%s 不应在 P-2 出结论前标记 Tested", sp.ID)
		}
	}
}
