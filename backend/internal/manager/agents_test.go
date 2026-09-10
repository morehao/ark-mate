package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// agentTestSvc 播 deepseekharness/opencode/echo（内置 agent 播种 + echo 自定义；不拨真实子进程）。
func agentTestSvc(t *testing.T, agentDir string) *Service {
	t.Helper()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "deepseekharness", Name: "deepseek-harness"},
		runtime.Spec{ID: "opencode", Name: "opencode"},
		runtime.Spec{ID: "echo", Name: "Echo"},
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

	// 注册自定义 agent（echo runtime 上的别名，不写代码新增 ACP agent）。
	resp, err := svc.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Name: "别名echo", Runtimes: []string{"echo"}, Provider: "openai:gpt-4o"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if resp.ID != "alias-echo" || resp.Provider != "openai:gpt-4o" {
		t.Fatalf("resp 不符: %+v", resp)
	}
	// 定义文件已落盘。
	if _, err := os.Stat(filepath.Join(dir, "alias-echo.json")); err != nil {
		t.Fatalf("definitions 未落盘: %v", err)
	}

	// 重启（新 Service + 同一目录）→ definitions 加载。
	svc2 := agentTestSvc(t, dir)
	if a, ok := svc2.AgentByID("alias-echo"); !ok || len(a.Runtimes) != 1 || a.Runtimes[0] != "echo" || a.Provider != "openai:gpt-4o" {
		t.Fatalf("重启后未加载 definition: %+v ok=%v", a, ok)
	}

	// PATCH 热更新 name。
	name := "别名echo-v2"
	u, err := svc2.UpdateAgent(ctx, "alias-echo", handler.AgentUpdateReq{Name: &name})
	if err != nil || u.Name != "别名echo-v2" {
		t.Fatalf("UpdateAgent: %v %+v", err, u)
	}
	if a, _ := svc2.AgentByID("alias-echo"); a.Name != "别名echo-v2" {
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

	// 非法：保留前缀 id 拒绝；重复 id 拒绝；未知 runtime 拒绝。
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "agent-x", Runtimes: []string{"echo"}}); err == nil {
		t.Fatal("保留前缀应拒绝")
	}
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err == nil {
		t.Fatal("重复 id 应拒绝")
	}
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "ghost", Runtimes: []string{"no-such-runtime"}}); err == nil {
		t.Fatal("未知 runtime 应拒绝")
	}

	// 删除自定义 + 定义文件清理。
	if _, err := svc2.DeleteAgent(ctx, "alias-echo"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := svc2.AgentByID("alias-echo"); ok {
		t.Fatal("删除后仍存在")
	}
	if _, err := os.Stat(filepath.Join(dir, "alias-echo.json")); !os.IsNotExist(err) {
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
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := svc2.SetDefaultAgent(ctx, "alias-echo"); err != nil {
		t.Fatalf("切到自定义 agent: %v", err)
	}
	if _, err := svc2.DeleteAgent(ctx, "alias-echo"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if got := svc2.DefaultAgentID(); got != "dsh" {
		t.Fatalf("删除当前 Agent 后应回落 dsh: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "default.json")); !os.IsNotExist(err) {
		t.Fatalf("default.json 应被清理: %v", err)
	}
}

// TestAgentRegistryLoadSkipsBroken 目录加载：坏 JSON / 未知 runtime 定义跳过不 panic。
func TestAgentRegistryLoadSkipsBroken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good-echo","name":"好","runtimes":["echo"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{not json`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "noruntime.json"), []byte(`{"id":"x1","runtimes":["nope"]}`), 0o644)
	svc := agentTestSvc(t, dir)
	if _, ok := svc.AgentByID("good-echo"); !ok {
		t.Fatal("good 应加载")
	}
	if _, ok := svc.AgentByID("x1"); ok {
		t.Fatal("未知 runtime 应跳过")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); err != nil {
		t.Fatalf("bad.json 应保留未动: %v", err)
	}
}

// TestAgentStartStopGuards 无编排器装配（demo/纯列表）时启停返回明确错误。
func TestAgentStartStopGuards(t *testing.T) {
	ctx := context.Background()
	svc := agentTestSvc(t, "")
	if _, err := svc.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartAgent(ctx, "alias-echo"); err == nil {
		t.Fatal("无编排器应拒绝 StartAgent")
	}
	if err := svc.StopAgent(ctx, "alias-echo"); err == nil {
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
	_ = os.WriteFile(filepath.Join(dir, "pol-echo.json"),
		[]byte(`{"id":"pol-echo","name":"策略echo","runtimes":["echo"],"permission_policy":{"default":"deny","tools":{"bash":"allow"}}}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad-pol.json"),
		[]byte(`{"id":"bad-pol","runtimes":["echo"],"permission_policy":{"tools":{"bash":"yes"}}}`), 0o644)
	svc := agentTestSvc(t, dir)

	a, ok := svc.AgentByID("pol-echo")
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
	var found *handler.AgentResp
	for i := range list {
		if list[i].ID == "pol-echo" {
			found = &list[i]
		}
	}
	if found == nil || found.PermissionPolicy == nil || found.PermissionPolicy.Tools["bash"] != "allow" {
		t.Fatalf("AgentResp 未暴露策略: %+v", found)
	}

	// PATCH 更新策略（并落盘）；B5 起含 native_mode（原生权限模式）。
	pol := &handler.PermissionPolicyResp{Default: "allow", Tools: map[string]string{"write": "deny"}, NativeMode: "danger-full-access"}
	u, err := svc.UpdateAgent(ctx, "pol-echo", handler.AgentUpdateReq{PermissionPolicy: pol})
	if err != nil || u.PermissionPolicy == nil || u.PermissionPolicy.Default != "allow" ||
		u.PermissionPolicy.NativeMode != "danger-full-access" {
		t.Fatalf("PATCH 策略失败: %v %+v", err, u)
	}
	// PATCH 非法策略 → acl request 类 + 稳定 code（REST 层映射 40001），且内存不变。
	bad := &handler.PermissionPolicyResp{Default: "sometimes"}
	var ae *acl.Error
	if _, err := svc.UpdateAgent(ctx, "pol-echo", handler.AgentUpdateReq{PermissionPolicy: bad}); err == nil {
		t.Fatal("非法策略应报错")
	} else if !errors.As(err, &ae) || ae.Code != "invalid_permission_policy" || ae.Class != acl.ClassRequest {
		t.Fatalf("非法策略应为 request/invalid_permission_policy，got %v", err)
	}
	// B5：非法 native_mode 同样被拒（不静默生效）。
	if _, err := svc.UpdateAgent(ctx, "pol-echo", handler.AgentUpdateReq{
		PermissionPolicy: &handler.PermissionPolicyResp{NativeMode: "yolo"},
	}); err == nil || !errors.As(err, &ae) || ae.Code != "invalid_permission_policy" {
		t.Fatalf("非法 native_mode 应报 invalid_permission_policy，got %v", err)
	}
	if a, _ := svc.AgentByID("pol-echo"); a.PermissionPolicy.Default != "allow" {
		t.Fatalf("非法 PATCH 不应改动内存策略: %+v", a.PermissionPolicy)
	}

	// 重启加载：策略随定义文件恢复。
	svc2 := agentTestSvc(t, dir)
	a2, ok := svc2.AgentByID("pol-echo")
	if !ok || a2.PermissionPolicy == nil || a2.PermissionPolicy.Default != "allow" ||
		a2.PermissionPolicy.Tools["write"] != "deny" || a2.PermissionPolicy.NativeMode != "danger-full-access" {
		t.Fatalf("重启后策略未恢复: %+v ok=%v", a2, ok)
	}

	// CreateAgent 带策略 → 落盘 + 可读回。
	created, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "new-pol", Runtimes: []string{"echo"},
		PermissionPolicy: &handler.PermissionPolicyResp{Default: "deny"}})
	if err != nil || created.PermissionPolicy == nil || created.PermissionPolicy.Default != "deny" {
		t.Fatalf("CreateAgent 带策略失败: %v %+v", err, created)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "new-pol.json"))
	if !strings.Contains(string(b), `"permission_policy"`) {
		t.Fatalf("策略未落盘: %s", b)
	}
}

// TestSeedAgentReadonly 种子 agent 只读不变量（修复：内置播种 id 由 agent-<runtime> 改为
// dsh/opencode 后，仅按前缀判定的守卫失效 → 种子可删、runtime 可改）。
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

	// 种子 runtime 不可改；name/provider/策略可改。
	rt := []string{"opencode"}
	var ae *acl.Error
	if _, err := svc.UpdateAgent(ctx, "dsh", handler.AgentUpdateReq{Runtimes: &rt}); err == nil {
		t.Fatal("种子 runtime 应不可修改")
	} else if !errors.As(err, &ae) || ae.Code != "seed_agent_readonly" {
		t.Fatalf("改 runtime 应 seed_agent_readonly，got %v", err)
	}
	name := "dsh（默认）"
	pol := &handler.PermissionPolicyResp{Default: "ask"}
	u, err := svc.UpdateAgent(ctx, "dsh", handler.AgentUpdateReq{Name: &name, PermissionPolicy: pol})
	if err != nil || u.Name != name || u.PermissionPolicy == nil || u.PermissionPolicy.Default != "ask" || !u.Seed {
		t.Fatalf("种子 name/策略应可改且 seed 仍为 true: %v %+v", err, u)
	}

	// 自定义 agent 仍可改 runtime / 删除。
	if _, err := svc.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	rt2 := []string{"echo", "opencode"}
	if _, err := svc.UpdateAgent(ctx, "alias-echo", handler.AgentUpdateReq{Runtimes: &rt2}); err != nil {
		t.Fatalf("自定义 agent runtime 应可改: %v", err)
	}
	if _, err := svc.DeleteAgent(ctx, "alias-echo"); err != nil {
		t.Fatalf("自定义 agent 应可删: %v", err)
	}
}

// TestDeleteAgentImpactDetachAndBusyRefusal 删除影响面（P2 删除面）：
// 列表外露 conversation_count；删除回带影响面并**立刻摘除**该 agent 的活跃会话（后续 prompt 报
// agent_not_found 且文案给出路），而不是「删了还能跑、重启后才失效」；有在途 turn 时整体拒绝且无副作用。
func TestDeleteAgentImpactDetachAndBusyRefusal(t *testing.T) {
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, eventbus.New())
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	svc.attachOrchestrator(orch)

	svc.agents["alias-echo"] = &AgentRec{ID: "alias-echo", Name: "别名 echo", Runtimes: []string{"echo"}}
	svc.agents["alias-idle"] = &AgentRec{ID: "alias-idle", Name: "空闲", Runtimes: []string{"echo"}}
	svc.registerConversation(&ConversationRec{ID: "c1", AgentID: "alias-echo", Status: ConversationStatusInProgress})
	svc.registerConversation(&ConversationRec{ID: "c2", AgentID: "alias-echo", Status: ConversationStatusInProgress})
	svc.registerConversation(&ConversationRec{ID: "c3", AgentID: "dsh", Status: ConversationStatusInProgress})

	agents, err := svc.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	counts := map[string]int{}
	for _, a := range agents {
		counts[a.ID] = a.ConversationCount
	}
	if counts["alias-echo"] != 2 || counts["alias-idle"] != 0 {
		t.Fatalf("conversation_count 不符（删除影响面）: %+v", counts)
	}

	// c1 是内存里仍活着的会话（连着底座）：删除必须立刻让它失效。
	orch.conversations["c1"] = &ActiveConversation{ID: "c1", AgentID: "alias-echo", Runtime: "echo",
		sess: &stubConversation{stop: "end_turn"}}
	orch.byProvider["native-1"] = "c1"

	resp, err := svc.DeleteAgent(ctx, "alias-echo")
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if resp.Deleted != "alias-echo" || resp.AffectedConversations != 2 || resp.DetachedConversations != 1 || resp.WasDefault {
		t.Fatalf("影响面不符: %+v", resp)
	}
	if _, ok := orch.conversations["c1"]; ok {
		t.Fatal("活跃会话应被摘除")
	}
	if _, ok := orch.byProvider["native-1"]; ok {
		t.Fatal("原生→平台映射应一并清除")
	}
	if _, ok := svc.AgentByID("alias-echo"); ok {
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

	// 有在途 turn → 整体拒绝，且不删定义、不摘会话（失败即无副作用）。
	svc.agents["alias-busy"] = &AgentRec{ID: "alias-busy", Name: "忙", Runtimes: []string{"echo"}}
	busy := &ActiveConversation{ID: "cb", AgentID: "alias-busy", Runtime: "echo", sess: &stubConversation{stop: "end_turn"}}
	busy.busy = true
	orch.conversations["cb"] = busy
	if _, err := svc.DeleteAgent(ctx, "alias-busy"); err == nil {
		t.Fatal("有在途 turn 应拒绝删除")
	} else if !errors.As(err, &ae) || ae.Code != "agent_busy" {
		t.Fatalf("应报 agent_busy，got %v", err)
	}
	if _, ok := svc.AgentByID("alias-busy"); !ok {
		t.Fatal("拒绝时不得删除定义")
	}
	if _, ok := orch.conversations["cb"]; !ok {
		t.Fatal("拒绝时不得摘除活跃会话")
	}
}

// TestCredentialMissingBlocksConversationStart agent 绑定悬空/无 key 的 provider 档案时显式失败
// （credential_missing + 出路），不静默以「无密钥」姿态起进程；补齐 key 后放行。
func TestCredentialMissingBlocksConversationStart(t *testing.T) {
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	svc := NewService(reg, eventbus.New())
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	svc.attachOrchestrator(orch)

	svc.agents["a-prov"] = &AgentRec{ID: "a-prov", Name: "P", Runtimes: []string{"echo"}, Provider: "gone"}
	err := svc.StartAgent(ctx, "a-prov")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "credential_missing" {
		t.Fatalf("悬空 provider 档案应报 credential_missing，got %v", err)
	}
	if !strings.Contains(ae.Details, "不存在") {
		t.Fatalf("文案应指明档案不存在并给出路: %s", ae.Details)
	}

	// 档案存在但无 key → 仍拒绝，并指出本应注入的变量名（可操作）。
	typ := "deepseek"
	if _, err := svc.UpdateProvider(ctx, "gone", handler.ProviderPatchReq{Type: &typ}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	err = svc.StartAgent(ctx, "a-prov")
	if !errors.As(err, &ae) || ae.Code != "credential_missing" || !strings.Contains(ae.Details, "DEEPSEEK_API_KEY") {
		t.Fatalf("无 key 档案应报 credential_missing 并指明注入变量，got %v", err)
	}

	// 补齐 key → 凭据面放行（不再报 credential_missing）。
	key := "sk-test"
	if _, err := svc.UpdateProvider(ctx, "gone", handler.ProviderPatchReq{Key: &key}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	a, _ := svc.AgentByID("a-prov")
	if err := svc.CredentialProblem(a); err != nil {
		t.Fatalf("补齐 key 后不应再报凭据问题: %v", err)
	}

	// 未绑定档案（provider 为空）→ 合法：用底座自身登录态，不报错。
	svc.agents["a-plain"] = &AgentRec{ID: "a-plain", Name: "Plain", Runtimes: []string{"echo"}}
	p, _ := svc.AgentByID("a-plain")
	if err := svc.CredentialProblem(p); err != nil {
		t.Fatalf("未绑定档案不应报凭据问题: %v", err)
	}
}
