package manager

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/ark-mate/pkg/runtime/deepseekharness"
	"github.com/morehao/golib/gerror"
)

// ——— M5 平台模型清单：写面失败面、每 runtime 一个默认、T2 回落、/agents 的派生只读 ———

// codeOf 把任意 error 归一成对外业务码（与信封里的 code 同源：控制器出口走的就是 FromError）。
func codeOf(err error) int {
	if err == nil {
		return 0
	}
	var ge gerror.Error
	if errors.As(code.FromError(err), &ge) {
		return ge.Code
	}
	return 0
}

// modelRig 一个带库、带 stub ACP、带 a1（runtime=stub）的架子 —— 但 stub **没有**声明
// ConfigProjection，所以它天然适合断言 E3 的"未声明即拒绝"。
type modelRig struct {
	*genRig
}

func newModelRig(t *testing.T) *modelRig {
	t.Helper()
	r := newGenRig(t) // stub ACP + 真库 + agent a1(runtime=stub)
	// 注册一个 runtime 供清单写面的正例使用：直接取**真实 adapter 的声明**（避免测试里手抄一份
	// "看起来一样"的声明），只把隔离 env 补上（真形态里它由 IsolationEnv 在装机期注入）。
	real := runtime.DefaultRegistry()
	oc, ok := real.Get("opencode")
	if !ok {
		t.Fatal("默认注册表里应有 opencode")
	}
	// **隔离纪律（硬约束）**：夹具注册的是真实 adapter 的声明，而 `ModelCatalog` 在没有活会话/缓存时
	// 会走 resume 分支**真的 spawn 底座**。若不隔离，那一次 spawn 会去读用户自己的
	// `~/.local/share/opencode/...`（2026-10-10 实测触发过一次）。两道保险：
	//   ① 注入隔离 env（home 全落在 t.TempDir 下）；
	//   ② 把 Command 换成一个**不存在**的桩名——真到 spawn 那一步只会失败，绝不落到用户装的那份。
	oc.Launch.Env = append(oc.Launch.Env,
		"OPENCODE_CONFIG_DIR="+filepath.Join(t.TempDir(), "opencode-config"))
	oc.Launch.Command = "mate-test-opencode-stub-not-on-path"
	r.svc.reg.ApplyIsolationEnv(t.TempDir())
	r.svc.reg.Register(oc)
	// dsh 也注册一份（同样把 Command 换成桩名）：H 阶段的逐 runtime 声明需要**两个**承载清单的底座
	// 才能验"只改一份不抹另一份"；而 dsh 的落点当前是只读的，注册它不会写任何东西。
	if dsh, ok := real.Get(deepseekharness.ID); ok {
		dsh.Launch.Command = "mate-test-dsh-stub-not-on-path"
		dsh.Launch.Env = append(dsh.Launch.Env, "DSH_HOME="+filepath.Join(t.TempDir(), "dsh-home"))
		r.svc.reg.Register(dsh)
	}
	return &modelRig{genRig: r}
}

// createModel 走真实写面建一条条目。
func (r *modelRig) createModel(t *testing.T, runtimeID, providerID, modelID string, mutate ...func(*dtomodel.ModelCreateReq)) *dtomodel.ModelResp {
	t.Helper()
	ctx := context.Background()
	key := "sk-test"
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: providerID, Type: "deepseek", Key: &key}); err != nil &&
		errCode(err) != "provider_exists" {
		t.Fatalf("CreateProvider(%s): %v", providerID, err)
	}
	req := dtomodel.ModelCreateReq{Runtime: runtimeID, ProviderID: providerID, ModelID: modelID}
	for _, m := range mutate {
		m(&req)
	}
	resp, err := r.svc.CreateModel(ctx, req)
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	return resp
}

// TestModelListFailureFaces 写面的四种失败面，错误码逐条等于设计表。
func TestModelListFailureFaces(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)

	// ① runtime 未声明 ConfigProjection → 40501 config_projection_unsupported（清单没有兑现通道）。
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Type: "deepseek", Key: &key}); err != nil {
		t.Fatal(err)
	}
	_, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "stub", ProviderID: "p1", ModelID: "m"})
	if errCode(err) != "config_projection_unsupported" {
		t.Fatalf("未声明 ConfigProjection 应 40501 config_projection_unsupported，got %q（%v）", errCode(err), err)
	}
	if got := codeOf(err); got != 40501 {
		t.Fatalf("该错误的对外码应为 40501，got %d", got)
	}

	// ② provider 不存在 → 40401 provider_not_found。
	_, err = r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "nope", ModelID: "m"})
	if errCode(err) != "provider_not_found" || codeOf(err) != 40401 {
		t.Fatalf("provider 不存在应 40401 provider_not_found，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}

	// ③ runtime 未注册 → 40401 runtime_not_found。
	_, err = r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "ghost", ProviderID: "p1", ModelID: "m"})
	if errCode(err) != "runtime_not_found" {
		t.Fatalf("runtime 未注册应 runtime_not_found，got %q", errCode(err))
	}

	// ④ 同三元组重复 → 40901 model_exists。
	r.createModel(t, "opencode", "p1", "v4-flash")
	_, err = r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "p1", ModelID: "v4-flash"})
	if errCode(err) != "model_exists" || codeOf(err) != 40901 {
		t.Fatalf("重复三元组应 40901 model_exists，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}

	// ⑤ PATCH/DELETE 不存在的条目 → 40401 model_not_found。
	if _, err := r.svc.UpdateModel(ctx, "no-such", dtomodel.ModelPatchReq{Name: ptr("x")}); errCode(err) != "model_not_found" {
		t.Fatalf("PATCH 不存在应 model_not_found，got %q", errCode(err))
	}
	if resp, err := r.svc.DeleteModel(ctx, "no-such"); err != nil || resp.Deleted {
		t.Fatalf("DELETE 不存在应幂等（deleted=false, code=0）: %+v err=%v", resp, err)
	}
}

// TestModelDefaultPerRuntimeAndDeleteGuard 每 runtime 恰好一条默认；删除默认被拒并给出出路。
func TestModelDefaultPerRuntimeAndDeleteGuard(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	a := r.createModel(t, "opencode", "p1", "m-a", func(req *dtomodel.ModelCreateReq) {
		req.IsDefault = ptr(true)
	})
	if !a.IsDefault || a.ModelID != "m-a" {
		t.Fatalf("首条应成为默认: %+v", a)
	}
	if a.Effect == nil || a.Effect.Effect != "next_session" {
		t.Fatalf("改默认应回 next_session（T2 快路径立即生效）: %+v", a.Effect)
	}
	if len(a.Effect.Channels) != 2 {
		t.Fatalf("改默认应同时给出两条通道: %+v", a.Effect.Channels)
	}
	b := r.createModel(t, "opencode", "p1", "m-b", func(req *dtomodel.ModelCreateReq) { req.IsDefault = ptr(true) })
	if !b.IsDefault {
		t.Fatalf("第二条设为默认后它应是默认: %+v", b)
	}
	// 恰好一条默认（跨 runtime 各一条）。
	items, err := r.svc.ListModels(ctx, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range items {
		if it.IsDefault {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("每 runtime 应恰好一条默认，实际 %d: %+v", n, items)
	}
	// 删默认 → 40501 model_is_default + 出路（不静默置空）。
	_, err = r.svc.DeleteModel(ctx, b.ID)
	if errCode(err) != "model_is_default" || codeOf(err) != 40501 {
		t.Fatalf("删除默认应 40501 model_is_default，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "别的条目") && !strings.Contains(err.Error(), "换成另一条") {
		t.Fatalf("文案应给出「先指定另一个默认」的出路: %v", err)
	}
	// 先换默认再删 → 成功。
	if _, err := r.svc.UpdateModel(ctx, a.ID, dtomodel.ModelPatchReq{IsDefault: ptr(true)}); err != nil {
		t.Fatalf("切默认: %v", err)
	}
	resp, err := r.svc.DeleteModel(ctx, b.ID)
	if err != nil || !resp.Deleted {
		t.Fatalf("换默认后应可删: %+v err=%v", resp, err)
	}
}

// TestModelValueForTwoAlgorithmsAgree E6：T5（写配置）与 T2（下发给会话）**共用同一个生成器**。
//
// 判据是"同一输入在两条算法里产出逐字节相同的值"——两个底座各一条。
func TestModelValueForTwoAlgorithmsAgree(t *testing.T) {
	// dsh：JSON.stringify([provider, model])。
	v, err := modelValueFor("deepseekharness", "deepseek-main", "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if v != `["deepseek-main","deepseek-v4-flash"]` {
		t.Fatalf("dsh 的模型值形态不符: %q", v)
	}
	var arr []string
	if err := json.Unmarshal([]byte(v), &arr); err != nil || len(arr) != 2 {
		t.Fatalf("dsh 的值应是合法 JSON 数组: %q err=%v", v, err)
	}
	// opencode：provider/model。
	v, err = modelValueFor("opencode", "deepseek-main", "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if v != "deepseek-main/deepseek-v4-flash" {
		t.Fatalf("opencode 的模型值形态不符: %q", v)
	}
	// 未声明的 runtime 不猜形状。
	if _, err := modelValueFor("some-third-runtime", "p", "m"); errCode(err) != "model_value_unsupported" {
		t.Fatalf("未声明的 runtime 应拒绝猜模型值形状，got %q", errCode(err))
	}
	// 清单里**不存** value：它只存生成 value 的输入（这里是 E6 的另一半判据）。
	// 另起一个架子（避免与上面的纯函数断言共用测试夹具的生命周期）。
	t.Run("body_has_no_value", func(t *testing.T) {
		r := newModelRig(t)
		resp := r.createModel(t, "opencode", "p1", "v4-flash")
		if resp.Value != "p1/v4-flash" {
			t.Fatalf("对外回带生成的 value: %q", resp.Value)
		}
		row, found, err := r.svc.st.GetModelDefinition(context.Background(), resp.ID)
		if err != nil || !found {
			t.Fatalf("读行: found=%v err=%v", found, err)
		}
		if strings.Contains(row.Body, "/") {
			t.Fatalf("清单正文里不得出现生成的值（只存输入）: %s", row.Body)
		}
	})
}

// TestAgentDefaultModelIsDerivedReadOnly E7：`GET /agents` 的 `default_model` 随 `is_default` **自动变**，
// 没有任何同步写逻辑；且按 `agent.runtimes[]` 求交（列表 → 按 runtime 索引）。
func TestAgentDefaultModelIsDerivedReadOnly(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	// a1 同时服务 stub 与 opencode；另有 a2 只服务 stub。
	r.svc.agents["a1"].Runtimes = []string{"stub", "opencode"}
	r.svc.agents["a2"] = &AgentRec{ID: "a2", Name: "A2", Runtimes: []string{"stub"}}

	m1 := r.createModel(t, "opencode", "p1", "m-1")
	m2 := r.createModel(t, "opencode", "p1", "m-2")

	// 每次都重新取一次列表：`default_model` 是**读时派生**的，拿旧快照断言等于在测缓存。
	find := func(id string) dtoagent.AgentResp {
		t.Helper()
		agents, err := r.svc.ListAgents()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range agents {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("agent %s 不在列表里", id)
		return dtoagent.AgentResp{}
	}
	// 还没有默认 → default_model 缺席（不是空 map）。
	if dm := find("a1").DefaultModel; len(dm) != 0 {
		t.Fatalf("尚无默认时不该有 default_model: %+v", dm)
	}
	// 设 m1 为默认 → 再取一次，**同一个 agent** 的值已经变了（无任何同步逻辑）。
	if _, err := r.svc.UpdateModel(ctx, m1.ID, dtomodel.ModelPatchReq{IsDefault: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if dm := find("a1").DefaultModel; dm["opencode"] != "m-1" {
		t.Fatalf("a1 的 opencode 默认应为 m-1: %+v", dm)
	}
	// 换成 m2 → 自动跟随。
	if _, err := r.svc.UpdateModel(ctx, m2.ID, dtomodel.ModelPatchReq{IsDefault: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if dm := find("a1").DefaultModel; dm["opencode"] != "m-2" {
		t.Fatalf("a1 的 opencode 默认应跟随 is_default 变为 m-2: %+v", dm)
	}
	// 求交：a2 不服务 opencode → 它没有该键（列表语义天然按 runtime 索引）。
	if dm := find("a2").DefaultModel; len(dm) != 0 {
		t.Fatalf("a2 不服务 opencode，不该有该 runtime 的默认: %+v", dm)
	}
	// 默认模型**不下发**给 opencode 之外的东西：stub 没有清单 → 它没有默认。
	if dm := find("a1").DefaultModel; len(dm) != 1 {
		t.Fatalf("只有 opencode 有默认，stub 没有: %+v", dm)
	}
}

// TestAgentWriteFaceRejectsDefaultModel E8：`POST/PATCH /agents` 显式拒绝 `default_model`。
func TestAgentWriteFaceRejectsDefaultModel(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	// POST 带 default_model → 40001 + 指路（**不是静默收下**）。
	_, err := r.svc.CreateAgent(ctx, dtoagent.AgentCreateReq{
		ID: "a-new", Runtimes: []string{"stub"},
		DefaultModel: json.RawMessage(`{"stub":"m-1"}`),
	})
	if errCode(err) != "default_model_is_derived" || codeOf(err) != 40001 {
		t.Fatalf("POST /agents 带 default_model 应 40001 拒绝，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "设置 · 模型") {
		t.Fatalf("文案应指路「设置 · 模型」: %v", err)
	}
	// PATCH 同判据。
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{
		DefaultModel: json.RawMessage(`{"stub":"m-1"}`),
	}); errCode(err) != "default_model_is_derived" {
		t.Fatalf("PATCH /agents/:id 带 default_model 应被拒，got %q（%v）", errCode(err), err)
	}
	// `null` 视为没带（可选键显式置空是常见写法，不该报错）。
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{
		DefaultModel: json.RawMessage(`null`),
	}); err != nil {
		t.Fatalf("default_model:null 不该报错: %v", err)
	}
}

// TestModelListRequiresSQLite memory 模式（无清单载体）→ 40501，而不是"内存生效、盘上没留痕"。
func TestModelListRequiresSQLite(t *testing.T) {
	ctx := context.Background()
	svc := NewService(runtime.DefaultRegistry(), eventbus.New())
	_, err := svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "p", ModelID: "m"})
	if errCode(err) != "model_list_unavailable" || codeOf(err) != 40501 {
		t.Fatalf("memory 模式的清单写面应 40501 model_list_unavailable，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	items, err := svc.ListModels(ctx, "")
	if err != nil || len(items) != 0 {
		t.Fatalf("memory 模式的清单读面应回空列表: %+v err=%v", items, err)
	}
}

// TestResolveDefaultModelFallbackChain E5（闸门 2 的单测部分）：回落链三条断言。
//
//	① 会话显式 model 优先于默认（不查默认）；
//	② 该 runtime 有默认 → DefaultModelValueFor 给出该值（T2 确实会下发）；
//	③ 该 runtime 无默认 → 空串（**不下发**，与旧行为逐字相同）。
func TestResolveDefaultModelFallbackChain(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	// ③ 无默认 → 空串。
	v, err := r.svc.DefaultModelValueFor(ctx, "opencode")
	if err != nil || v != "" {
		t.Fatalf("无默认应回空串（不下发）: %q err=%v", v, err)
	}
	m := r.createModel(t, "opencode", "p1", "m-a", func(req *dtomodel.ModelCreateReq) {
		req.IsDefault = ptr(true)
	})
	// ② 有默认 → 生成的值（T2 下发的就是它）。
	v, err = r.svc.DefaultModelValueFor(ctx, "opencode")
	if err != nil || v != "p1/m-a" {
		t.Fatalf("有默认应回生成的值: %q err=%v", v, err)
	}
	if m.Value != v {
		t.Fatalf("清单里回带的 value 与下发用的值必须逐字同源: %q vs %q", m.Value, v)
	}
	// 没有清单的 runtime（stub）→ 空串（内存模式/未声明清单的底座都不下发）。
	if v, err := r.svc.DefaultModelValueFor(ctx, "stub"); err != nil || v != "" {
		t.Fatalf("未声明清单的 runtime 应回空串: %q err=%v", v, err)
	}
}

// TestModelListProviderDeleteGuard provider 被模型条目挂靠时删除被拒（两类引用都要交出来）。
func TestModelListProviderDeleteGuard(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	m := r.createModel(t, "opencode", "p1", "m-a")
	_ = m
	_, err := r.svc.DeleteProvider(ctx, "p1")
	if errCode(err) != "provider_in_use" {
		t.Fatalf("被模型条目挂靠的 provider 应 provider_in_use，got %q（%v）", errCode(err), err)
	}
	if !strings.Contains(err.Error(), "模型条目") {
		t.Fatalf("文案应交出模型条目这一类引用: %v", err)
	}
}

// TestDefaultModelReasoningEffortNotSent E5 的④′：本版**不下发** effort（模型与努力级别在 ACP 上是
// 两个独立 option，平台首版只管 category=model）。这条钉住"没有偷偷多发一个 option"。
func TestDefaultModelReasoningEffortNotSent(t *testing.T) {
	// 判据落在类型上：T2 下发的载荷是 `runtime.ModelConfig`，它只有一个 Model 字段——
	// 加 effort 必须改这个类型，那时这条注释与测试都会被人看见。
	var mc runtime.ModelConfig
	b, err := json.Marshal(mc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "effort") {
		t.Fatalf("T2 的载荷里不该有 effort（本版只管 category=model）: %s", b)
	}
	// dsh adapter 的启动参数在 M5c 之前保持逐字不变（E 不碰它）。
	reg := runtime.DefaultRegistry()
	sp, ok := reg.Get(deepseekharness.ID)
	if !ok {
		t.Fatal("dsh 应已注册")
	}
	if len(sp.ConfigProjection.Format) == 0 {
		t.Fatal("dsh 应已声明模型配置落点（供 E3 判定）")
	}
	_ = testutil.StubRegistry() // 保持 testutil 依赖（夹具在别处复用）
}

// TestClearDefaultModel PATCH 的两个方向都要生效。
//
// 这条来自一次**端到端探针**：删掉 dsh 的 overlay 之后想恢复，只删文件不够——平台侧仍声明着默认，
// T2 会继续把它下发、底座已经不认识了，于是每条新会话都失败；而"清掉默认"这个动作当时是**静默无效**的
// （写面只处理 `is_default:true`），于是恢复了半天以为自己没清干净。
func TestClearDefaultModel(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	created := r.createModel(t, "opencode", "p1", "m1")
	row := created.ID
	// 置默认。
	if _, err := r.svc.UpdateModel(ctx, row, dtomodel.ModelPatchReq{IsDefault: boolPtr(true)}); err != nil {
		t.Fatalf("置默认: %v", err)
	}
	if v, err := r.svc.DefaultModelValueFor(ctx, "opencode"); err != nil || v == "" {
		t.Fatalf("置默认后应有默认值: %q %v", v, err)
	}
	// 清默认 —— 必须真的清掉。
	if _, err := r.svc.UpdateModel(ctx, row, dtomodel.ModelPatchReq{IsDefault: boolPtr(false)}); err != nil {
		t.Fatalf("清默认: %v", err)
	}
	if v, err := r.svc.DefaultModelValueFor(ctx, "opencode"); err != nil || v != "" {
		t.Fatalf("清默认后不该再有默认值: %q %v", v, err)
	}
	// 幂等：本来就不是默认 → 仍然成功。
	if _, err := r.svc.UpdateModel(ctx, row, dtomodel.ModelPatchReq{IsDefault: boolPtr(false)}); err != nil {
		t.Fatalf("重复清默认应幂等: %v", err)
	}
	// 审计里要留下"清默认"那一版（一行 + 一条审计同事务的纪律）：版本号至少推进了两次。
	got, found, err := r.svc.st.GetModelDefinition(ctx, row)
	if err != nil || !found {
		t.Fatalf("读行: found=%v err=%v", found, err)
	}
	if got.Revision < 2 {
		t.Fatalf("置默认与清默认都应各推进一版 revision，实际 %d", got.Revision)
	}
	if got.IsDefault {
		t.Fatal("清默认后列上的 is_default 应为 false")
	}
}
