package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/dto/dtoagent"
	"github.com/morehao/ark-forge/internal/dto/dtolibrary"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/internal/service/svclibrary"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// ——— M6-6：agent 定义的 DB 载体 ———
//
// 这一组只针对 **sqlite 载体**（memory 载体的既有用例在 agents_test.go，原样保留且绿）；
// 两者共同的"种子只读""非零字段覆盖"语义在两边各有一份断言，因为它们的实现确实不同
// （文件是整份重写、行是 typed marshal 后覆盖）。

// agentDBRig 一个"装配期已从 store 装载"的 Service + 其库（可直连断言行与审计）。
func agentDBRig(t *testing.T, dir string) (*Service, *store.Store) {
	t.Helper()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "deepseekharness", Name: "deepseek-harness"},
		runtime.Spec{ID: "opencode", Name: "opencode"},
		runtime.Spec{ID: "stub", Name: "Stub"},
	)
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	if dir != "" {
		svc.LoadAgentDefinitionsFromStore(context.Background(), dir)
	}
	return svc, st
}

// mustSeedAgentRow 直接往库里写一行 agent 定义（模拟"存量文件已导入"的落点）。
func mustSeedAgentRow(t *testing.T, st *store.Store, id, body string) {
	t.Helper()
	if ok, err := st.UpsertAgentDefinition(context.Background(), id, body, 0, "import"); err != nil || !ok {
		t.Fatalf("写 agent 行 %s: ok=%v err=%v", id, ok, err)
	}
}

// TestAgentDefinitionsStoreRoundTrip 注册 → 覆盖 → 切默认 → 删除，全程落在 agents 表 + 审计里，
// 且内存与载体始终一致（失败即无副作用）。
func TestAgentDefinitionsStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, st := agentDBRig(t, t.TempDir())

	resp, err := svc.CreateAgent(ctx, dtoagent.AgentCreateReq{
		ID: "alias-stub", Name: "别名stub", Runtimes: []string{"stub"}, Provider: "openai:gpt-4o",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if resp.ID != "alias-stub" || resp.Provider != "openai:gpt-4o" || resp.Seed {
		t.Fatalf("resp 不符: %+v", resp)
	}
	row, found, err := st.GetAgentDefinition(ctx, "alias-stub")
	if err != nil || !found {
		t.Fatalf("库里应有定义行: found=%v err=%v", found, err)
	}
	if row.Revision != 1 {
		t.Fatalf("新建行的 revision 应为 1，got %d", row.Revision)
	}
	var def map[string]any
	if err := json.Unmarshal([]byte(row.Body), &def); err != nil {
		t.Fatalf("行正文不是 JSON: %v（%s）", err, row.Body)
	}
	if def["name"] != "别名stub" || def["provider"] != "openai:gpt-4o" {
		t.Fatalf("行正文缺字段: %+v", def)
	}
	if rs, _ := def["runtimes"].([]any); len(rs) != 1 || rs[0] != "stub" {
		t.Fatalf("行正文 runtimes 不符: %+v", def["runtimes"])
	}
	// 不落文件（DB 载体的定义不在盘上）。
	if _, err := os.Stat(filepath.Join(svc.agentDirSnapshot(), "alias-stub.json")); !os.IsNotExist(err) {
		t.Fatalf("sqlite 载体不该写定义文件: %v", err)
	}

	// PATCH name：行 revision 抬升，runtimes/provider 不被抹掉。
	name := "别名stub-v2"
	if _, err := svc.UpdateAgent(ctx, "alias-stub", dtoagent.AgentUpdateReq{Name: &name}); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	row, _, _ = st.GetAgentDefinition(ctx, "alias-stub")
	if row.Revision != 2 || !strings.Contains(row.Body, "别名stub-v2") ||
		!strings.Contains(row.Body, `"stub"`) || !strings.Contains(row.Body, "openai:gpt-4o") {
		t.Fatalf("覆盖写后的行不符: rev=%d body=%s", row.Revision, row.Body)
	}
	revs, err := st.ListCapabilityRevisions(ctx, model.CapabilityKindAgent, "alias-stub")
	if err != nil || len(revs) != 2 || revs[0].Action != model.CapabilityActionCreate || revs[1].Action != model.CapabilityActionUpdate {
		t.Fatalf("agent 审计不符: %+v err=%v", revs, err)
	}
	if revs[1].Actor != definitionWriteActor {
		t.Fatalf("agent 审计的 actor 应是 REST 写面: %+v", revs[1])
	}

	// 切换「当前 Agent」：落 meta（而不是任何定义文件）。
	if _, err := svc.SetDefaultAgent(ctx, "alias-stub"); err != nil {
		t.Fatalf("SetDefaultAgent: %v", err)
	}
	if id, ok, _ := st.GetDefaultAgentID(ctx); !ok || id != "alias-stub" {
		t.Fatalf("meta.default_agent 不符: id=%q ok=%v", id, ok)
	}
	if _, err := os.Stat(filepath.Join(svc.agentDirSnapshot(), "default.json")); !os.IsNotExist(err) {
		t.Fatalf("sqlite 载体不该写 default.json: %v", err)
	}

	// 删除：行消失、审计留痕、显式选择被连带清除、内存与磁盘都不留。
	del, err := svc.DeleteAgent(ctx, "alias-stub")
	if err != nil || del.Deleted != "alias-stub" || !del.WasDefault {
		t.Fatalf("DeleteAgent: %+v err=%v", del, err)
	}
	if _, found, _ := st.GetAgentDefinition(ctx, "alias-stub"); found {
		t.Fatal("删除后库里不该还有行")
	}
	if _, ok, _ := st.GetDefaultAgentID(ctx); ok {
		t.Fatal("删除当前 Agent 后应清除 meta.default_agent（回落内置默认）")
	}
	if _, ok := svc.AgentByID("alias-stub"); ok {
		t.Fatal("删除后内存里不该还有")
	}
	revs, _ = st.ListCapabilityRevisions(ctx, model.CapabilityKindAgent, "alias-stub")
	if len(revs) != 3 || revs[2].Action != model.CapabilityActionDelete || revs[2].Digest != "" {
		t.Fatalf("删除审计不符（删定义也留痕且 digest 为空）: %+v", revs)
	}
	if got := svc.DefaultAgentID(); got != "dsh" {
		t.Fatalf("删除当前 Agent 后应回落内置默认 dsh，got %q", got)
	}
}

// TestAgentStoreLoaderSeedMerge 装载的**部分合并**语义（计划 §7 P-7 的护栏）：
// 行里只有 name 时，种子的 runtimes 必须原样保留——"整行 update 把种子字段抹空"是最容易丢的一条。
func TestAgentStoreLoaderSeedMerge(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	// 只覆盖 name 的一行（种子 dsh 的 runtime 不写进行里）。
	mustSeedAgentRow(t, st, "dsh", `{"id":"dsh","name":"我的 dsh"}`)
	// 一个用户 agent（自带走全部字段）。
	mustSeedAgentRow(t, st, "u1", `{"id":"u1","name":"U1","runtimes":["stub"]}`)

	svc, _ := agentDBRig(t, t.TempDir())
	// 上面 rig 用的是新库，这里换成预置过行的库再装载（装载路径只有一条）。
	svc.EnableStore(st)
	if n := svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir()); n != 2 {
		t.Fatalf("装载应采纳 2 条，got %d", n)
	}
	seed, ok := svc.AgentByID("dsh")
	if !ok {
		t.Fatal("种子 dsh 应仍在")
	}
	if seed.Name != "我的 dsh" {
		t.Fatalf("行里的 name 应覆盖种子: %+v", seed)
	}
	if len(seed.Runtimes) != 1 || seed.Runtimes[0] != "deepseekharness" {
		t.Fatalf("行里没写 runtimes → 必须保留种子的 runtimes（否则运行时直接不可用）: %+v", seed)
	}
	if !seedAgent(seed.ID) {
		t.Fatal("dsh 仍是种子（只读判定不得因为有了行而失效）")
	}
	if u, ok := svc.AgentByID("u1"); !ok || len(u.Runtimes) != 1 || u.Runtimes[0] != "stub" {
		t.Fatalf("用户 agent 未装载正确: %+v ok=%v", u, ok)
	}

	// 行被删掉（面板删除或清库）→ 重新装载不得留下幽灵 agent。
	if ok, err := st.DeleteAgentDefinition(ctx, "u1", 1, "rest"); err != nil || !ok {
		t.Fatalf("删行: ok=%v err=%v", ok, err)
	}
	if n := svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir()); n != 1 {
		t.Fatalf("重新装载应只剩 1 条: %d", n)
	}
	if _, ok := svc.AgentByID("u1"); ok {
		t.Fatal("库里删掉的行不得以幽灵 agent 留在内存里")
	}
	if _, ok := svc.AgentByID("dsh"); !ok {
		t.Fatal("种子不该被整体替换掉")
	}
}

// TestSeedAgentReadonlyOnStore 有了行之后种子仍然只读：改 runtime / 删除都被拒，
// **且行本身一个字节都不动**（拒绝必须无副作用）。
func TestSeedAgentReadonlyOnStore(t *testing.T) {
	ctx := context.Background()
	svc, st := agentDBRig(t, t.TempDir())
	mustSeedAgentRow(t, st, "dsh", `{"id":"dsh","name":"我的 dsh"}`)
	if n := svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir()); n != 1 {
		t.Fatalf("装载应采纳 1 条，got %d", n)
	}
	before, _, _ := st.GetAgentDefinition(ctx, "dsh")

	runtimes := []string{"stub"}
	if _, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{Runtimes: &runtimes}); err == nil {
		t.Fatal("改种子 runtime 应被拒")
	} else if got := errCode(err); got != "seed_agent_readonly" {
		t.Fatalf("应是 seed_agent_readonly，got %q（%v）", got, err)
	}
	if _, err := svc.DeleteAgent(ctx, "dsh"); err == nil {
		t.Fatal("删种子应被拒")
	} else if got := errCode(err); got != "seed_agent_readonly" {
		t.Fatalf("应是 seed_agent_readonly，got %q（%v）", got, err)
	}
	after, _, _ := st.GetAgentDefinition(ctx, "dsh")
	if after.Revision != before.Revision || after.Body != before.Body {
		t.Fatalf("被拒的写面不该动行: before=%+v after=%+v", before, after)
	}
	if a, _ := svc.AgentByID("dsh"); a.Name != "我的 dsh" || a.Runtimes[0] != "deepseekharness" {
		t.Fatalf("被拒的写面不该动内存: %+v", a)
	}

	// 但元数据可以改（种子的 name 允许覆盖）——这与文件模式一致（可直接编辑 definitions）。
	name := "换个名字"
	if _, err := svc.UpdateAgent(ctx, "dsh", dtoagent.AgentUpdateReq{Name: &name}); err != nil {
		t.Fatalf("改种子 name 应放行: %v", err)
	}
	row, _, _ := st.GetAgentDefinition(ctx, "dsh")
	if row.Revision != before.Revision+1 || !strings.Contains(row.Body, "换个名字") {
		t.Fatalf("改 name 应落到行上: rev=%d body=%s", row.Revision, row.Body)
	}
}

// TestDefaultAgentFromStoreFallback 显式选择指向一个**已被删除**的 agent 时按回落链生效，
// 且不清除记录（定义回来即自动恢复）——与 default.json 的既有语义逐字一致。
func TestDefaultAgentFromStoreFallback(t *testing.T) {
	ctx := context.Background()
	svc, st := agentDBRig(t, t.TempDir())
	if err := st.SetDefaultAgentID(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if n := svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir()); n != 0 {
		t.Fatalf("没有定义行时应采纳 0 条，got %d", n)
	}
	if got := svc.DefaultAgentID(); got != "dsh" {
		t.Fatalf("指向未注册 agent 时应回落内置默认: %q", got)
	}
	if id, ok, _ := st.GetDefaultAgentID(ctx); !ok || id != "gone" {
		t.Fatalf("不该清除悬空选择（定义回来即恢复）: id=%q ok=%v", id, ok)
	}

	// 定义"回来" → 重新装载后选择生效。
	mustSeedAgentRow(t, st, "gone", `{"id":"gone","name":"回来了","runtimes":["stub"]}`)
	svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir())
	if got := svc.DefaultAgentID(); got != "gone" {
		t.Fatalf("定义回来后显式选择应生效: %q", got)
	}
}

// TestAgentLibraryOriginOnStore Library 的 agent 来源按载体分派：有行 → `db:agent/<id>`，
// 无行（种子）→ 空串（origin = imported，与文件模式的"没有定义文件"同解）。
func TestAgentLibraryOriginOnStore(t *testing.T) {
	ctx := context.Background()
	svc, st := agentDBRig(t, t.TempDir())
	mustSeedAgentRow(t, st, "u1", `{"id":"u1","name":"U1","runtimes":["stub"]}`)
	svc.LoadAgentDefinitionsFromStore(ctx, t.TempDir())

	items, err := svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindAgent})
	if err != nil {
		t.Fatalf("ListLibrary: %v", err)
	}
	byID := map[string]string{}
	for _, it := range items {
		byID[it.ID] = it.SourceFile
	}
	if byID["u1"] != "db:agent/u1" {
		t.Fatalf("有行的 agent 来源应是定位串: %q", byID["u1"])
	}
	if byID["dsh"] != "" {
		t.Fatalf("种子（无行）的来源应为空: %q", byID["dsh"])
	}
}
