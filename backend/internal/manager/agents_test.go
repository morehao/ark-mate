package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// agentTestSvc 播 dsh/opencode/echo（内置 agent 播种 + echo 自定义；不拨真实子进程）。
func agentTestSvc(t *testing.T, agentDir string) *Service {
	t.Helper()
	// 夹具的 spec 要带上**凭据交付面声明**（v12）：`providerEnvFor` 按各底座自己的
	// `Credential` 决定"走原生文档还是注入 env"，没有声明的底座它一律不注入。
	// 这里给 opencode 声明 env 通道（与真实 adapter 同形），dsh 声明原生文档
	// （与真实 adapter 同形：dsh 的密钥走 .credentials.yaml，平台不重复注入 env）。
	// 夹具的 spec 要带上**凭据交付面声明**（v12）：`providerEnvFor` 按各底座自己的
	// `Credential` 决定"走原生文档还是注入 env"，没有声明的底座它一律不注入。
	//
	// 两个声明都**直接取真实 adapter 的那一份**（不在这里手抄路径/模板）——手抄会把底座的
	// 私有知识（凭据文档路径）带进平台内核，`TestInternalHasNoRuntimeConfigLiterals` 会判红，
	// 而且抄出来的那份迟早与 adapter 漂移。
	real := runtime.AllBuiltinRegistry()
	dhSpec, dshOK := real.Get("dsh")
	ocSpec, ocOK := real.Get("opencode")
	if !dshOK || !ocOK {
		t.Fatal("默认注册表里应有 dsh 与 opencode")
	}
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "dsh", Name: "dsh", Credential: dhSpec.Credential},
		runtime.Spec{ID: "opencode", Name: "opencode", Credential: ocSpec.Credential},
		runtime.Spec{ID: "stub", Name: "Stub"},
	)
	svc := NewService(reg, eventbus.New())
	if agentDir != "" {
		svc.LoadAgentDefinitions(agentDir)
	}
	return svc
}

// TestAgentRegistryDefinitionsCRUD P2 definitions：注册持久化 + 重启（新 Service）加载 +
// PATCH 热更新 + 删除；内置 agent dsh/opencode 由播种提供（dsh 默认）。
func TestAgentRegistryDefinitionsCRUD(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	// 注册自定义 agent（对**当前底座**的一份命名配置，不写代码新增 ACP agent）。
	resp, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "alias-stub", Name: "别名stub", Provider: "openai:gpt-4o"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if resp.ID != "alias-stub" || resp.Provider != "openai:gpt-4o" {
		t.Fatalf("resp 不符: %+v", resp)
	}
	// 定义文件已落盘。
	if _, err := os.Stat(filepath.Join(dir, "alias-stub.json")); err != nil {
		t.Fatalf("definitions 未落盘: %v", err)
	}

	// 重启（新 Service + 同一目录）→ definitions 加载。
	svc2 := agentTestSvc(t, dir)
	if a, ok := svc2.AgentByID("alias-stub"); !ok || a.Provider != "openai:gpt-4o" {
		t.Fatalf("重启后未加载 definition: %+v ok=%v", a, ok)
	}

	// PATCH 热更新 name。
	name := "别名stub-v2"
	u, err := svc2.UpdateAgent(ctx, "alias-stub", dtoagent.AgentUpdateReq{Name: &name})
	if err != nil || u.Name != "别名stub-v2" {
		t.Fatalf("UpdateAgent: %v %+v", err, u)
	}
	if a, _ := svc2.AgentByID("alias-stub"); a.Name != "别名stub-v2" {
		t.Fatalf("name 未更新: %+v", a)
	}

	// 内置 Agent 已播种（用户选 Agent 而非 runtime；未切换前 dsh 为「当前 Agent」）。
	if a, ok := svc2.AgentByID("dsh"); !ok || a == nil {
		t.Fatalf("默认 agent dsh 应播种: %+v ok=%v", a, ok)
	}
	if a, ok := svc2.AgentByID("opencode"); !ok || a == nil {
		t.Fatalf("内置 agent opencode 应播种: %+v ok=%v", a, ok)
	}
	if got := svc2.DefaultAgentID(); got != "dsh" {
		t.Fatalf("未切换前当前 Agent 应为 dsh: %s", got)
	}

	// 非法：保留前缀 id 拒绝；重复 id 拒绝。
	// （"未知 runtime 拒绝"随候选面一起下线：agent 不再声明底座，未知 id 无处可存。）
	if _, err := svc2.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "agent-x"}); err == nil {
		t.Fatal("保留前缀应拒绝")
	}
	if _, err := svc2.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "alias-stub"}); err == nil {
		t.Fatal("重复 id 应拒绝")
	}

	// 删除自定义 + 定义文件清理。
	if _, err := svc2.DeleteAgent(ctx, "alias-stub"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := svc2.AgentByID("alias-stub"); ok {
		t.Fatal("删除后仍存在")
	}
	if _, err := os.Stat(filepath.Join(dir, "alias-stub.json")); !os.IsNotExist(err) {
		t.Fatalf("定义文件未清理: %v", err)
	}
}

// TestSetDefaultAgentSwitchAndPersist「当前 Agent」切换（Agents 页「设为当前」的服务端语义）：
// is_default 跟随切换、选择落 <dir>/default.json、重启后仍生效、删除当前 agent 后回落内置种子。
func TestSetDefaultAgentSwitchAndPersist(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	// 未切换：内置回落 dsh。
	if got := svc.DefaultAgentID(); got != "dsh" {
		t.Fatalf("缺省当前 Agent 应为 dsh: %s", got)
	}
	resp, err := svc.SetDefaultAgent(ctx, "opencode")
	if err != nil || resp.ID != "opencode" || !resp.IsDefault {
		t.Fatalf("SetDefaultAgent: %v %+v", err, resp)
	}
	// ListAgents 是权威面（面板按钮显隐与新建会话预选都读它）。
	agents, err := svc.ListAgents()
	if err != nil || len(agents) != 2 {
		t.Fatalf("ListAgents: %v %+v", err, agents)
	}
	for _, a := range agents {
		if want := a.ID == "opencode"; a.IsDefault != want {
			t.Fatalf("is_default 未跟随切换: %+v", a)
		}
	}
	// 幂等：重复切换同一 agent 不报错、状态不变。
	if again, err := svc.SetDefaultAgent(ctx, "opencode"); err != nil || !again.IsDefault {
		t.Fatalf("重复切换应幂等: %v %+v", err, again)
	}

	// 落盘：<dir>/default.json（目录级配置，不得被当作 agent 定义加载）。
	b, err := os.ReadFile(filepath.Join(dir, "default.json"))
	if err != nil {
		t.Fatalf("default.json 未落盘: %v", err)
	}
	if !strings.Contains(string(b), `"default_agent": "opencode"`) {
		t.Fatalf("default.json 内容不符: %s", b)
	}
	// 重启（新 Service + 同目录）→ 选择仍生效，且 agents 数不变（default.json 非定义）。
	svc2 := agentTestSvc(t, dir)
	if got := svc2.DefaultAgentID(); got != "opencode" {
		t.Fatalf("重启后当前 Agent 应为 opencode: %s", got)
	}
	if list, _ := svc2.ListAgents(); len(list) != 2 {
		t.Fatalf("default.json 被误当定义加载: %+v", list)
	}

	// 未知 agent → agent_not_found（REST 40401）。
	if _, err := svc2.SetDefaultAgent(ctx, "nope"); err == nil {
		t.Fatal("未知 agent 应报错")
	}

	// 删除「当前 Agent」→ 清除显式选择 + 回落内置种子（default.json 一并清理）。
	if _, err := svc2.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "alias-stub"}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := svc2.SetDefaultAgent(ctx, "alias-stub"); err != nil {
		t.Fatalf("切到自定义 agent: %v", err)
	}
	if _, err := svc2.DeleteAgent(ctx, "alias-stub"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if got := svc2.DefaultAgentID(); got != "dsh" {
		t.Fatalf("删除当前 Agent 后应回落 dsh: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "default.json")); !os.IsNotExist(err) {
		t.Fatalf("default.json 应被清理: %v", err)
	}
}

// TestAgentRegistryLoadToleratesDeprecatedRuntimeKeys 目录加载：坏 JSON 跳过不 panic；
// 而带已下线键（`runtimes`）的老定义**照常装载**——读面宽容、写面拒绝（见
// TestCreateAgentRejectsRuntimeSelection）。这与"静默丢掉整个 agent"是两回事：老文件里的
// 名字/provider/策略都还有效，把它整条扔掉才是真正的破坏。
func TestAgentRegistryLoadToleratesDeprecatedRuntimeKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good-stub","name":"好","runtimes":["stub"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{not json`), 0o644)
	// 老文件里写着一个早就没人认得的 id：底座是构建期常量，这个键不参与任何判断。
	_ = os.WriteFile(filepath.Join(dir, "noruntime.json"), []byte(`{"id":"x1","name":"老定义","runtimes":["nope"]}`), 0o644)
	svc := agentTestSvc(t, dir)
	if _, ok := svc.AgentByID("good-stub"); !ok {
		t.Fatal("good 应加载")
	}
	if a, ok := svc.AgentByID("x1"); !ok || a.Name != "老定义" {
		t.Fatalf("带已下线 runtimes 键的老定义应照常装载（读面宽容）: %+v ok=%v", a, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); err != nil {
		t.Fatalf("bad.json 应保留未动: %v", err)
	}
}

// TestAgentStartStopGuards 无编排器装配（demo/纯列表）时启停返回明确错误。
func TestAgentStartStopGuards(t *testing.T) {
	ctx := context.Background()
	svc := agentTestSvc(t, "")
	if _, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "alias-stub"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartAgent(ctx, "alias-stub"); err == nil {
		t.Fatal("无编排器应拒绝 StartAgent")
	}
	if err := svc.StopAgent(ctx, "alias-stub"); err == nil {
		t.Fatal("无编排器应拒绝 StopAgent")
	}
	if err := svc.StartAgent(ctx, "nope"); err == nil {
		t.Fatal("未知 agent 应拒绝")
	}
}

// TestAgentPermissionPolicyPersist M2 #7：permission_policy 定义加载 → AgentResp 只读面 →
// PATCH 校验/热更新 → 落盘 → 重启可恢复；非法动作的定义文件被跳过（不静默生效）。
func TestAgentPermissionPolicyPersist(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	_ = os.MkdirAll(dir, 0o755)
	// 定义文件带策略 + 一个非法策略（应跳过）。
	_ = os.WriteFile(filepath.Join(dir, "pol-stub.json"),
		[]byte(`{"id":"pol-stub","name":"策略stub","permission_policy":{"default":"deny","tools":{"bash":"allow"}}}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad-pol.json"),
		[]byte(`{"id":"bad-pol","permission_policy":{"tools":{"bash":"yes"}}}`), 0o644)
	svc := agentTestSvc(t, dir)

	a, ok := svc.AgentByID("pol-stub")
	if !ok || a.PermissionPolicy == nil || a.PermissionPolicy.Default != "deny" ||
		a.PermissionPolicy.Decide("bash", "execute") != PermissionAllow {
		t.Fatalf("策略定义未加载: %+v ok=%v", a, ok)
	}
	if _, ok := svc.AgentByID("bad-pol"); ok {
		t.Fatal("非法策略定义应被跳过")
	}

	// REST 只读面（GET /agents 的 AgentResp）暴露策略。
	list, err := svc.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	var found *dtoagent.AgentResp
	for i := range list {
		if list[i].ID == "pol-stub" {
			found = &list[i]
		}
	}
	if found == nil || found.PermissionPolicy == nil || found.PermissionPolicy.Tools["bash"] != "allow" {
		t.Fatalf("AgentResp 未暴露策略: %+v", found)
	}

	// PATCH 更新策略（并落盘）；B5 起含 native_mode（原生权限模式）。
	pol := &dtoagent.PermissionPolicyResp{Default: "allow", Tools: map[string]string{"write": "deny"}, NativeMode: "danger-full-access"}
	u, err := svc.UpdateAgent(ctx, "pol-stub", dtoagent.AgentUpdateReq{PermissionPolicy: pol})
	if err != nil || u.PermissionPolicy == nil || u.PermissionPolicy.Default != "allow" ||
		u.PermissionPolicy.NativeMode != "danger-full-access" {
		t.Fatalf("PATCH 策略失败: %v %+v", err, u)
	}
	// PATCH 非法策略 → acl request 类 + 稳定 code（REST 层映射 40001），且内存不变。
	bad := &dtoagent.PermissionPolicyResp{Default: "sometimes"}
	var ae *acl.Error
	if _, err := svc.UpdateAgent(ctx, "pol-stub", dtoagent.AgentUpdateReq{PermissionPolicy: bad}); err == nil {
		t.Fatal("非法策略应报错")
	} else if !errors.As(err, &ae) || ae.Code != "invalid_permission_policy" || ae.Class != acl.ClassRequest {
		t.Fatalf("非法策略应为 request/invalid_permission_policy，got %v", err)
	}
	// B5：非法 native_mode 同样被拒（不静默生效）。
	if _, err := svc.UpdateAgent(ctx, "pol-stub", dtoagent.AgentUpdateReq{
		PermissionPolicy: &dtoagent.PermissionPolicyResp{NativeMode: "yolo"},
	}); err == nil || !errors.As(err, &ae) || ae.Code != "invalid_permission_policy" {
		t.Fatalf("非法 native_mode 应报 invalid_permission_policy，got %v", err)
	}
	if a, _ := svc.AgentByID("pol-stub"); a.PermissionPolicy.Default != "allow" {
		t.Fatalf("非法 PATCH 不应改动内存策略: %+v", a.PermissionPolicy)
	}

	// 重启加载：策略随定义文件恢复。
	svc2 := agentTestSvc(t, dir)
	a2, ok := svc2.AgentByID("pol-stub")
	if !ok || a2.PermissionPolicy == nil || a2.PermissionPolicy.Default != "allow" ||
		a2.PermissionPolicy.Tools["write"] != "deny" || a2.PermissionPolicy.NativeMode != "danger-full-access" {
		t.Fatalf("重启后策略未恢复: %+v ok=%v", a2, ok)
	}

	// CreateAgent 带策略 → 落盘 + 可读回。
	created, err := svc2.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "new-pol",
		PermissionPolicy: &dtoagent.PermissionPolicyResp{Default: "deny"}})
	if err != nil || created.PermissionPolicy == nil || created.PermissionPolicy.Default != "deny" {
		t.Fatalf("CreateAgent 带策略失败: %v %+v", err, created)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "new-pol.json"))
	if !strings.Contains(string(b), `"permission_policy"`) {
		t.Fatalf("策略未落盘: %s", b)
	}
}

// TestSeedAgentReadonly 种子 agent 只读不变量（修复：内置播种 id 由 agent-<runtime> 改为
// dsh/opencode 后，仅按前缀判定的守卫失效 → 种子可删）。
//
// v12 起"种子 runtime 不可改"这条随字段一起下线：agent 没有 runtime 可选，改它一律是
// `runtime_selection_removed`（对所有 agent 一视同仁，不是"种子特权"）。
func TestSeedAgentReadonly(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	// 列表暴露权威 seed 标记（前端不再按 id 前缀猜）。
	list, err := svc.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	seeds := map[string]bool{}
	for _, a := range list {
		seeds[a.ID] = a.Seed
	}
	if !seeds["dsh"] || !seeds["opencode"] {
		t.Fatalf("内置播种应标记 seed: %+v", seeds)
	}

	// 内置种子不可删（此前会成功删除，导致默认 agent 缺失、新建会话失败）。
	for _, id := range []string{"dsh", "opencode"} {
		var ae *acl.Error
		if _, err := svc.DeleteAgent(ctx, id); err == nil {
			t.Fatalf("种子 %q 应不可删除", id)
		} else if !errors.As(err, &ae) || ae.Code != "seed_agent_readonly" {
			t.Fatalf("种子 %q 删除应 seed_agent_readonly，got %v", id, err)
		}
		if _, ok := svc.AgentByID(id); !ok {
			t.Fatalf("种子 %q 应仍在注册表", id)
		}
	}
	// 保留前缀（历史种子形态）同样只读。
	if _, err := svc.DeleteAgent(ctx, "agent-legacy"); err == nil {
		t.Fatal("agent- 前缀应不可删除")
	}

	// 底座不可选：带 `runtimes` 的 PATCH 被显式拒绝（种子也不例外）。
	var ae *acl.Error
	if _, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{Runtimes: json.RawMessage(`["opencode"]`)}); err == nil {
		t.Fatal("带 runtimes 的 PATCH 应被拒（底座由构建期固定）")
	} else if !errors.As(err, &ae) || ae.Code != "runtime_selection_removed" {
		t.Fatalf("带 runtimes 应 runtime_selection_removed，got %v", err)
	}
	name := "dsh（默认）"
	pol := &dtoagent.PermissionPolicyResp{Default: "ask"}
	u, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{Name: &name, PermissionPolicy: pol})
	if err != nil || u.Name != name || u.PermissionPolicy == nil || u.PermissionPolicy.Default != "ask" || !u.Seed {
		t.Fatalf("种子 name/策略应可改且 seed 仍为 true: %v %+v", err, u)
	}

	// 自定义 agent 仍可改元数据 / 删除（底座不在此列：它对所有 agent 都是构建期常量）。
	if _, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "alias-stub"}); err != nil {
		t.Fatal(err)
	}
	alias := "别名"
	if _, err := svc.UpdateAgent(ctx, "alias-stub", dtoagent.AgentUpdateReq{Name: &alias}); err != nil {
		t.Fatalf("自定义 agent 元数据应可改: %v", err)
	}
	if _, err := svc.DeleteAgent(ctx, "alias-stub"); err != nil {
		t.Fatalf("自定义 agent 应可删: %v", err)
	}
}

// TestDeleteAgentImpactDetachAndBusyRefusal 删除影响面（P2 删除面）：
// 列表外露 session_count；删除回带影响面并**立刻摘除**该 agent 的活跃会话（后续 prompt 报
// agent_not_found 且文案给出路），而不是「删了还能跑、重启后才失效」；有在途 run 时整体拒绝且无副作用。
func TestDeleteAgentImpactDetachAndBusyRefusal(t *testing.T) {
	ctx := context.Background()
	reg := testutil.StubRegistry()
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub：底座必须与它一致
	svc := NewService(reg, eventbus.New())
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	svc.attachOrchestrator(orch)

	svc.agents["alias-stub"] = &AgentRec{ID: "alias-stub", Name: "别名 echo"}
	svc.agents["alias-idle"] = &AgentRec{ID: "alias-idle", Name: "空闲"}
	svc.registerSession(&SessionRec{ID: "c1", AgentID: "alias-stub", Status: SessionStatusInProgress})
	svc.registerSession(&SessionRec{ID: "c2", AgentID: "alias-stub", Status: SessionStatusInProgress})
	svc.registerSession(&SessionRec{ID: "c3", AgentID: "dsh", Status: SessionStatusInProgress})

	agents, err := svc.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	counts := map[string]int{}
	for _, a := range agents {
		counts[a.ID] = a.SessionCount
	}
	if counts["alias-stub"] != 2 || counts["alias-idle"] != 0 {
		t.Fatalf("session_count 不符（删除影响面）: %+v", counts)
	}

	// c1 是内存里仍活着的会话（连着底座）：删除必须立刻让它失效。
	orch.sessions["c1"] = &ActiveSession{ID: "c1", AgentID: "alias-stub", Runtime: "stub",
		sess: &stubSession{stop: "end_turn"}}
	orch.byProvider["native-1"] = "c1"

	resp, err := svc.DeleteAgent(ctx, "alias-stub")
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if resp.Deleted != "alias-stub" || resp.AffectedSessions != 2 || resp.DetachedSessions != 1 || resp.WasDefault {
		t.Fatalf("影响面不符: %+v", resp)
	}
	if _, ok := orch.sessions["c1"]; ok {
		t.Fatal("活跃会话应被摘除")
	}
	if _, ok := orch.byProvider["native-1"]; ok {
		t.Fatal("原生→平台映射应一并清除")
	}
	if _, ok := svc.AgentByID("alias-stub"); ok {
		t.Fatal("定义应已删除")
	}
	// 已摘除的会话继续提问 → agent_not_found，且给出「同 id 重新注册」的出路（历史仍可读）。
	_, err = orch.Prompt(ctx, "c1", "hi")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "agent_not_found" {
		t.Fatalf("摘除后应报 agent_not_found，got %v", err)
	}
	if !strings.Contains(ae.Details, "重新注册") {
		t.Fatalf("错误文案应给出恢复出路: %s", ae.Details)
	}

	// 有在途 run → 整体拒绝，且不删定义、不摘会话（失败即无副作用）。
	svc.agents["alias-busy"] = &AgentRec{ID: "alias-busy", Name: "忙"}
	busy := &ActiveSession{ID: "cb", AgentID: "alias-busy", Runtime: "stub", sess: &stubSession{stop: "end_turn"}}
	busy.busy = true
	orch.sessions["cb"] = busy
	if _, err := svc.DeleteAgent(ctx, "alias-busy"); err == nil {
		t.Fatal("有在途 run 应拒绝删除")
	} else if !errors.As(err, &ae) || ae.Code != "agent_busy" {
		t.Fatalf("应报 agent_busy，got %v", err)
	}
	if _, ok := svc.AgentByID("alias-busy"); !ok {
		t.Fatal("拒绝时不得删除定义")
	}
	if _, ok := orch.sessions["cb"]; !ok {
		t.Fatal("拒绝时不得摘除活跃会话")
	}
}

// TestCredentialMissingBlocksSessionStart agent 绑定悬空/无 key 的 provider 档案时显式失败
// （credential_missing + 出路），不静默以「无密钥」姿态起进程；补齐 key 后放行。
func TestCredentialMissingBlocksSessionStart(t *testing.T) {
	ctx := context.Background()
	reg := testutil.StubRegistry()
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub：底座必须与它一致
	svc := NewService(reg, eventbus.New())
	// v13：没有可用 vault 时**拒绝**存密钥（fail-closed），所以这条"补齐 key"的用例必须
	// 有一个真实（且私有）的 mate home——目录在 t.TempDir 下，绝不碰用户环境。
	svc.LoadAgentDefinitions(filepath.Join(t.TempDir(), "agents"))
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	svc.attachOrchestrator(orch)

	svc.agents["a-prov"] = &AgentRec{ID: "a-prov", Name: "P", Provider: "gone"}
	err := svc.StartAgent(ctx, "a-prov")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "credential_missing" {
		t.Fatalf("悬空 provider 档案应报 credential_missing，got %v", err)
	}
	if !strings.Contains(ae.Details, "不存在") {
		t.Fatalf("文案应指明档案不存在并给出路: %s", ae.Details)
	}

	// 档案存在但无 key → 仍拒绝，并**指出这条供应商**（v12 不再有"某个变量名"那回事：
	// 引用名是逐供应商派生的，改密钥就是改它）。
	if _, err := svc.CreateProvider(ctx, providerReq("gone", nil)); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	err = svc.StartAgent(ctx, "a-prov")
	if !errors.As(err, &ae) || ae.Code != "credential_missing" || !strings.Contains(ae.Details, "gone") {
		t.Fatalf("无 key 供应商应报 credential_missing 并指明它，got %v", err)
	}

	// 补齐 key → 凭据面放行（不再报 credential_missing）。
	key := "sk-test"
	if _, err := svc.UpdateProvider(ctx, "gone", dtoprovider.ProviderPatchReq{APIKey: &key}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	a, _ := svc.AgentByID("a-prov")
	if err := svc.CredentialProblem(a); err != nil {
		t.Fatalf("补齐 key 后不应再报凭据问题: %v", err)
	}

	// 未绑定档案（provider 为空）→ 合法：用底座自身登录态，不报错。
	svc.agents["a-plain"] = &AgentRec{ID: "a-plain", Name: "Plain"}
	p, _ := svc.AgentByID("a-plain")
	if err := svc.CredentialProblem(p); err != nil {
		t.Fatalf("未绑定档案不应报凭据问题: %v", err)
	}
}

// TestCreateAgentRejectsRuntimeSelection 写面显式拒绝 `runtimes` / `routing_policy`（40001）。
//
// 为什么是"拒绝"而不是"忽略"：JSON 绑定默认丢弃未知键，静默收下会让这两个键变成
// 「写了、存了、什么都不发生」的骗人字段——用户会以为"我换了底座"。`null` 视为"没带"
// （与 default_model 同一条纪律）。
func TestCreateAgentRejectsRuntimeSelection(t *testing.T) {
	ctx := context.Background()
	svc := agentTestSvc(t, filepath.Join(t.TempDir(), "agents"))

	bad := func(name string, err error) {
		t.Helper()
		var ae *acl.Error
		if err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
		if !errors.As(err, &ae) || ae.Code != "runtime_selection_removed" || ae.Class != acl.ClassRequest {
			t.Fatalf("%s 应是 request/runtime_selection_removed，got %v", name, err)
		}
		if !strings.Contains(ae.Details, "构建期") {
			t.Fatalf("%s 的文案应指路「底座由构建期固定」: %s", name, ae.Details)
		}
	}
	bad("CreateAgent 带 runtimes", func() error {
		_, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "a-deny-rt", Runtimes: json.RawMessage(`["opencode"]`)})
		return err
	}())
	bad("CreateAgent 带 routing_policy", func() error {
		_, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{ID: "a-deny-pol", RoutingPolicy: json.RawMessage(`"first_available"`)})
		return err
	}())
	bad("UpdateAgent 带 runtimes", func() error {
		_, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{Runtimes: json.RawMessage(`["opencode"]`)})
		return err
	}())
	bad("UpdateAgent 带 routing_policy", func() error {
		_, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{RoutingPolicy: json.RawMessage(`"capability_routed"`)})
		return err
	}())
	// 被拒即无副作用：两个键一个也没落进内存/定义文件。
	if _, err := os.Stat(filepath.Join(svc.agentDirSnapshot(), "a-deny-rt.json")); !os.IsNotExist(err) {
		t.Fatalf("被拒的创建不该留下定义文件: %v", err)
	}
	if _, ok := svc.AgentByID("a-deny-rt"); ok {
		t.Fatal("被拒的创建不该登记 agent")
	}
	// `null` = 没带（不报错），与"显式选择某个底座"是两回事。
	if _, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{
		Runtimes: json.RawMessage(`null`), RoutingPolicy: json.RawMessage(`null`),
	}); err != nil {
		t.Fatalf("null 应视为没带，不该报错: %v", err)
	}
}
