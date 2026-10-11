package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— v11 provider 写面：CRUD 的失败面、写面顺序与补偿、生效回执 ———
//
// A1 的四条端点 × 成功/冲突/不存在/被引用，错误码逐条等于设计表：
//
//	已存在          → 40901 provider_exists
//	不存在（PATCH） → 40401 provider_not_found
//	被引用（DELETE）→ 40901 provider_in_use + referenced_by_agents[]
//	id 非法         → 40001 invalid_provider_id

func TestProviderWriteFaceCRUD(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	r := newRig(t) // 带编排器（生效面回执需要它）
	svc := r.Svc
	svc.LoadAgentDefinitions(dir) // 设 agentDir（= vault 锚点）

	key := "sk-abc"
	// ① POST 成功。
	resp, err := svc.CreateProvider(ctx, func() dtoprovider.ProviderCreateReq {
		r := providerReq("deepseek-main", &key)
		r.Note = ptr("主账号")
		return r
	}())
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if resp.ID != "deepseek-main" || resp.Protocol != "openai" || resp.BaseURL == "" || !resp.HasKey || resp.Note != "主账号" {
		t.Fatalf("新建响应不符: %+v", resp)
	}
	if resp.Revision != 0 {
		t.Fatalf("memory 模式的 revision 应为 0（文件载体没有版本号），got %d", resp.Revision)
	}
	// ② POST 冲突 → provider_exists(40901)。
	if _, err := svc.CreateProvider(ctx, providerReq("deepseek-main", nil)); errCode(err) != "provider_exists" {
		t.Fatalf("重复新建应 provider_exists，got %q（%v）", errCode(err), err)
	}
	// ③ id 非法 → invalid_provider_id。
	for _, id := range []string{"", "../x", "a/b"} {
		if _, err := svc.CreateProvider(ctx, providerReq(id, nil)); errCode(err) != "invalid_provider_id" {
			t.Fatalf("id %q 应 invalid_provider_id，got %q", id, errCode(err))
		}
	}
	// ④ PATCH 不存在 → provider_not_found(40401)。
	if _, err := svc.UpdateProvider(ctx, "nope", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.example.com/v1")}); errCode(err) != "provider_not_found" {
		t.Fatalf("PATCH 不存在应 provider_not_found，got %q（%v）", errCode(err), err)
	}

	// ⑤ PATCH 只改 note → effect=none（不驱动行为的字段不许说"下一轮生效"）。
	done, err := svc.UpdateProvider(ctx, "deepseek-main", dtoprovider.ProviderPatchReq{Note: ptr("改了备注")})
	if err != nil {
		t.Fatalf("PATCH note: %v", err)
	}
	if done.Note != "改了备注" || done.Effect == nil || done.Effect.Effect != "none" {
		t.Fatalf("只改 note 应回 effect=none: %+v", done.Effect)
	}
	if done.Effect.Channel != "dial_env" {
		t.Fatalf("通道应为 dial_env: %+v", done.Effect)
	}

	// ⑥ PATCH 端点 → effect=next_turn（驱动行为的字段）。
	changed, err := svc.UpdateProvider(ctx, "deepseek-main", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.other.example/v1")})
	if err != nil {
		t.Fatalf("PATCH base_url: %v", err)
	}
	if changed.Effect == nil || changed.Effect.Effect != "next_turn" {
		t.Fatalf("改端点应回 effect=next_turn: %+v", changed.Effect)
	}
	// 引用名**只跟 id 有关**（逐供应商派生）——这正是 A12 要的不变量。
	// 这里用纯函数断言（夹具的注册表是空的，`providerEnvFor` 需要一个声明了 `RefPattern`
	// 的 runtime 才有意义；那条路径由 `TestProviderRegistryWriteOnceReadNever` 端到端覆盖）。
	ref := resource.RefFor("MATE_%s_API_KEY", "deepseek-main")
	if !strings.HasPrefix(ref, "MATE_DEEPSEEK_MAIN_") || !strings.HasSuffix(ref, "_API_KEY") {
		t.Fatalf("v12 的引用名应是逐供应商派生的：%q", ref)
	}
	if resource.RefFor("MATE_%s_API_KEY", "deepseek_main") == ref {
		t.Fatalf("规范化会碰撞的两个 id 必须靠哈希后缀区分开：%q", ref)
	}

	// ⑦ DELETE 幂等：第一次 true，第二次 false 且 code=0（不报错）。
	del, err := svc.DeleteProvider(ctx, "deepseek-main")
	if err != nil || !del.Deleted {
		t.Fatalf("删除应成功: %+v err=%v", del, err)
	}
	again, err := svc.DeleteProvider(ctx, "deepseek-main")
	if err != nil || again.Deleted {
		t.Fatalf("重复删除应幂等（deleted=false, code=0）: %+v err=%v", again, err)
	}
	// ⑧ 密文随之删除（不留孤儿）。
	if _, err := os.Stat(filepath.Join(home, vault.DirName, "providers", "deepseek-main.enc")); !os.IsNotExist(err) {
		t.Fatalf("删除档案应删掉密文: %v", err)
	}
	// ⑨ 删被引用的档案 → provider_in_use(40901) + referenced_by_agents[]。
	if _, err := svc.CreateProvider(ctx, providerReq("inuse", nil)); err != nil {
		t.Fatal(err)
	}
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Provider: "inuse"}
	err = nil
	if _, err = svc.DeleteProvider(ctx, "inuse"); errCode(err) != "provider_in_use" {
		t.Fatalf("被引用的档案应 provider_in_use，got %q（%v）", errCode(err), err)
	}
	if !strings.Contains(err.Error(), "a1") {
		t.Fatalf("文案应交出引用名单: %v", err)
	}
	// 解绑后即可删。
	svc.agents["a1"].Provider = ""
	if del, err := svc.DeleteProvider(ctx, "inuse"); err != nil || !del.Deleted {
		t.Fatalf("解绑后应可删: %+v err=%v", del, err)
	}
}

// TestProviderCreateCompensatesVaultOnCarrierFailure H4 故障注入（新建路径）：
// 让载体提交失败 → **刚写进 vault 的密文必须被删掉**，不留孤儿。
//
// 注入手法：直接在载体里放一行同 id 的档案（内存里没有）——写面的 `CreateCAS` 因此未命中，
// 走的正是"密文已写、载体没提交"那条补偿分支，且不需要任何 mock。
func TestProviderCreateCompensatesVaultOnCarrierFailure(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.LoadAgentDefinitionsFromStore(ctx, dir)

	// 载体里先放一行（内存不知道）→ 写面读内存判定"不存在"，于是走新建 → CAS 未命中。
	if ok, err := st.UpsertProviderDefinition(ctx, store.ProviderColumns{ID: "ghost", Protocol: "openai"}, 0, "rest"); err != nil || !ok {
		t.Fatalf("预置行: ok=%v err=%v", ok, err)
	}
	key := "sk-ghost"
	_, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "ghost", Protocol: "openai", APIKey: &key})
	if errCode(err) != "provider_exists" {
		t.Fatalf("CAS 未命中应翻成 provider_exists，got %q（%v）", errCode(err), err)
	}
	// 关键断言：密文被补偿删掉（否则每次失败的写面都留一份孤儿密文）。
	vst, verr := vault.Open(home)
	if verr != nil {
		t.Fatalf("vault.Open: %v", verr)
	}
	if _, gerr := vst.Get(providerRef("ghost"), vault.KindProviderKey); !errors.Is(gerr, vault.ErrNotFound) {
		t.Fatalf("补偿应删掉刚写入的密文，got err=%v", gerr)
	}
	if _, err := os.Stat(filepath.Join(home, vault.DirName, "providers", "ghost.enc")); !os.IsNotExist(err) {
		t.Fatalf("密文文件不该残留: %v", err)
	}
	// 载体那一行保持原样（写面失败 = 一行一审计都不落）。
	row, found, _ := st.GetProviderDefinition(ctx, "ghost")
	if !found || row.Protocol != "openai" || row.BaseURL != "" || row.Revision != 1 {
		t.Fatalf("失败不该改到载体: found=%v row=%+v", found, row)
	}
}

// TestProviderUpdateCompensatesVaultOnCarrierFailure H4 故障注入（覆盖路径）：
// 载体 CAS 失败 → vault 被**补偿回旧密文**（绝不出现"行是旧的、密钥是新的"）。
func TestProviderUpdateCompensatesVaultOnCarrierFailure(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.LoadAgentDefinitionsFromStore(ctx, dir)

	old := "sk-old"
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", APIKey: &old}); err != nil {
		t.Fatal(err)
	}
	// 让内存里的 revision 过期（模拟"有人在这中间改过载体"）：直接抬高库里的版本。
	if ok, err := st.UpsertProviderDefinition(ctx, store.ProviderColumns{ID: "p1", Protocol: "openai"}, 1, "rest"); err != nil || !ok {
		t.Fatalf("抬高版本: ok=%v err=%v", ok, err)
	}
	neu := "sk-new"
	_, err := svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{APIKey: &neu})
	if errCode(err) != "definition_changed" {
		t.Fatalf("CAS 未命中应 definition_changed，got %q（%v）", errCode(err), err)
	}
	// vault 必须回到旧密文（补偿），而不是留着新的。
	vst, verr := vault.Open(home)
	if verr != nil {
		t.Fatalf("vault.Open: %v", verr)
	}
	got, gerr := vst.Get(providerRef("p1"), vault.KindProviderKey)
	if gerr != nil {
		t.Fatalf("补偿后密文应可解: %v", gerr)
	}
	if string(got) != old {
		t.Fatalf("补偿应写回旧密文，got %q（期望 %q）", string(got), old)
	}
	// 内存也没被改脏（写面的承诺：载体提交成功之前内存一个字段都不动）。
	if env := svc.providerEnvFor("opencode", "p1"); len(env) != 1 || !strings.HasSuffix(env[0], "_API_KEY="+old) {
		t.Fatalf("失败的写面不该改内存: %v", env)
	}
}

// TestProviderEffectIsSameSourceAsGenerationPlan C5/A2：生效面回执与代际裁决**同源**。
//
// 判据不是"返回了两个数字"，而是：改 `type` 之后
//
//	① 空闲会话的句柄代际键**真的换了**（下一轮拿到新 env）；
//	② 忙会话的裁决就是 `planGenerationSwitch` 给出的 `defer`（回执里的 deferred 与它逐条一致）。
func TestProviderEffectIsSameSourceAsGenerationPlan(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t) // stub ACP（真子进程）+ 真库 + agent a1
	key := "sk-old"
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", APIKey: &key}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	r.svc.agents["a1"].Provider = "p1"

	idleID, idle := r.start(t, nil)
	busyID, busy := r.start(t, nil)

	// ② 忙的那条：有在途轮次。
	busy.mu.Lock()
	busy.busy = true
	busy.mu.Unlock()

	// 走 env 通道的底座上，改**密钥**才会换基础键（改了它才谈得上"下一轮拿到新 env"）。
	neu := "sk-new"
	changed, err := r.svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{APIKey: &neu})
	if err != nil {
		t.Fatalf("PATCH 密钥: %v", err)
	}
	eff := changed.Effect
	if eff == nil {
		t.Fatal("写面必须回带生效面（effect）")
	}
	if eff.Channel != "dial_env" || eff.Effect != "next_turn" {
		t.Fatalf("通道/时刻不符: %+v", eff)
	}
	if len(eff.AppliesToAgents) != 1 || eff.AppliesToAgents[0] != "a1" {
		t.Fatalf("受影响 agent 不符: %+v", eff.AppliesToAgents)
	}
	if eff.AffectedSessions != 2 || eff.Deferred != 1 {
		t.Fatalf("受影响/顺延计数不符: %+v", eff)
	}
	// 同源：逐会话调 planGenerationSwitch，裁决必须与回执一致。
	for _, tc := range []struct {
		id   string
		as   *ActiveSession
		want generationAction
	}{
		{idleID, idle, genActionSwitch},
		{busyID, busy, genActionDefer},
	} {
		rec := r.svc.sessionRec(tc.id)
		plan := r.orch.planGenerationSwitch(tc.as, rec)
		if !plan.PermChanged {
			t.Fatalf("%s: 改供应商端点必须让基础键变化（这正是'下一轮生效'的判据）", tc.id)
		}
		if plan.Action != tc.want {
			t.Fatalf("%s: 裁决应为 %s，got %s（%s）", tc.id, tc.want, plan.Action, plan.Reason)
		}
	}

	// ① 空闲会话在轮次边界真的换代际（新连接），忙的那条保持旧句柄。
	if got := r.boundary(t, idleID); got == idle {
		t.Fatal("空闲会话在轮次边界应迁到新代际（拿到新 env 的新进程）")
	}
	if got := r.boundary(t, busyID); got != busy {
		t.Fatal("有在途轮次的会话必须顺延（仍用旧句柄）")
	}
}

// TestProviderEffectNilWithoutOrchestrator 没有编排器时**不编数**（回 nil 而不是回 0）。
func TestProviderEffectNilWithoutOrchestrator(t *testing.T) {
	ctx := context.Background()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.LoadAgentDefinitions(filepath.Join(t.TempDir(), "agents"))
	if _, err := svc.CreateProvider(ctx, providerReq("p1", nil)); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.other.example/v1")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Effect != nil {
		t.Fatalf("没有编排器时不该编出生效面数字（宁可缺席）: %+v", resp.Effect)
	}
}

// TestProviderProblemsSurfaceBeforeSession G3：`problems[]` 让用户在开会话**之前**就看到问题。
func TestProviderProblemsSurfaceBeforeSession(t *testing.T) {
	ctx := context.Background()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.LoadAgentDefinitions(filepath.Join(t.TempDir(), "agents"))
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "no-key", Protocol: "openai"}); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.ListProviders(context.Background())
	if len(items) != 1 {
		t.Fatalf("列表面不符: %+v", items)
	}
	// v12 的问题清单不再有"本应注入哪个变量名"这回事（引用名是逐供应商派生的），
	// 但它必须仍然给出**可执行的出路**。
	if len(items[0].Problems) == 0 || !strings.Contains(strings.Join(items[0].Problems, " "), "密钥") {
		t.Fatalf("无密钥的供应商应给出可执行问题: %+v", items[0].Problems)
	}
	if !strings.Contains(strings.Join(items[0].Problems, " "), "设置 · 模型") {
		t.Fatalf("问题文案应指向设置 · 模型: %+v", items[0].Problems)
	}
	// 引用面是只读派生：agent 一引用就出现。底座维度只有**构建期那一个**（不再是逐 agent 收集候选）。
	setBuildRuntime(t, "opencode") // v12ProviderRegistry 只注册了 opencode
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Provider: "no-key"}
	items, _ = svc.ListProviders(context.Background())
	if len(items[0].Refs.Agents) != 1 || items[0].Refs.Agents[0] != "a1" ||
		len(items[0].Refs.Runtimes) != 1 || items[0].Refs.Runtimes[0] != "opencode" {
		t.Fatalf("引用面不符: %+v", items[0].Refs)
	}
}

// TestCredentialProblemPointsAtModelSettings C6：三条文案的出路都指向「设置 · 模型」。
func TestCredentialProblemPointsAtModelSettings(t *testing.T) {
	ctx := context.Background()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.LoadAgentDefinitions(filepath.Join(t.TempDir(), "agents"))

	// ① 悬空引用。
	err := svc.CredentialProblem(&AgentRec{ID: "a1", Provider: "gone"})
	if errCode(err) != "credential_missing" || !strings.Contains(err.Error(), "设置 · 模型") {
		t.Fatalf("悬空引用应报 credential_missing 并指向设置 · 模型: %v", err)
	}
	// ② 存在但无密钥。
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "nokey", Protocol: "openai"}); err != nil {
		t.Fatal(err)
	}
	err = svc.CredentialProblem(&AgentRec{ID: "a1", Provider: "nokey"})
	if errCode(err) != "credential_missing" || !strings.Contains(err.Error(), "设置 · 模型") ||
		!strings.Contains(err.Error(), "nokey") {
		t.Fatalf("无密钥应报 credential_missing（指明是哪条供应商）并指向设置 · 模型: %v", err)
	}
}
