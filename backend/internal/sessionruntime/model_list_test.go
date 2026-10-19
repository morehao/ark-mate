package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	defn "github.com/morehao/ark-mate/internal/definition"
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/model"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/provider"

	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/object/objid"
	"github.com/morehao/ark-mate/internal/resource"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/golib/gerror"
)

// ——— v12 应用层模型清单：写面失败面、**单一**默认、T2 回落 ———
//
// 与 v11 的差别是这条测试线的主题：模型**不带底座维度**，于是 v11 里那套"runtime 必须已声明
// `ConfigProjection`"的前置校验、逐 runtime 的默认、以及"删默认被拒"全部消失。

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

// modelRig 一个带 sqlite 库（= 有清单载体）的架子。
//
// v12 起清单写面**只**要求两点：载体是 sqlite、供应商存在——不再要求"某个 runtime 已声明
// 模型配置落点"。所以这个架子比 v11 简单得多，也不再需要在夹具里去 spawn 底座
// （那曾是本文件里最容易碰到用户真实目录的一处，现在结构上不可能）。
type modelRig struct {
	*genRig
}

func newModelRig(t *testing.T) *modelRig {
	t.Helper()
	r := newGenRig(t) // stub ACP + 真库（构建期底座 = stub）
	// 再注册两个**真实 adapter 的声明**（取默认注册表的那一份，避免测试里手抄一份"看起来一样"
	// 的声明）：默认模型要摊到"已注册 runtime"，对账要借服务该 runtime 的 agent 读目录。
	//
	// **隔离纪律（硬约束）**：`ModelCatalog` 在没有活会话/缓存时会走 resume 分支**真的 spawn
	// 底座**。夹具必须同时做两件事：① 注入隔离 env（home 全落在 t.TempDir 下）；② 把 Command
	// 换成一个**不存在**的桩名——真到 spawn 那一步只会失败，绝不落到用户装的那份。
	home := t.TempDir()
	// 内置那一份：Spec + `RegisterBuiltinPorts` 的三张执行表（缺了后者，这些底座会**静默**少掉
	// 原生权限 / 清单交付 / 模型值三面，表现为一堆看起来像"平台侧没实现"的假症状）。
	builtin := builtinSpec(t, runtime.DefaultBuildRuntime)
	builtin.Launch.Command = "mate-test-" + builtin.ID + "-stub-not-on-path"
	r.svc.planes.Registry.Register(builtin)
	dsh.New().RegisterPorts(r.svc.planes.Registry)
	// 合成那一份：同样 Spec 与端口同批登记（`runtimetest.Register` 就是夹具侧的 `RegisterBuiltinPorts`）。
	runtimetest.Register(r.svc.planes.Registry, runtime.LaunchSpec{Command: "mate-test-" + runtimetest.ID + "-stub-not-on-path"})
	// 隔离面由各 spec 自己声明（dsh 的 `DSH_HOME` / 合成底座的 `RTX_HOME`），内核只做机械投影。
	r.svc.planes.Registry.ApplyIsolationEnv(home)
	// v19：对账不再借 agent 读目录（agent 面撤销），夹具只剩"注册表 + 隔离 env"两件事。
	return &modelRig{genRig: r}
}

// builtinSpec 取一份内置适配的**真实声明**（注册表副本，改它不影响默认注册表）。
//
// 夹具借真实声明是为了"对账读的落点就是产品会写的那一个"，但**第二个底座不再借**：
// 随第二个真实 adapter 下线，仓库里已没有别的真实 adapter 可借，而 synthesize 一份反而是更诚实的
// 形态（runtimetest 的声明是合成的，所以它随适配面增减不会静默改变这些用例的覆盖面）。
func builtinSpec(t *testing.T, id string) runtime.Spec {
	t.Helper()
	sp, ok := dsh.Registry().Get(id)
	if !ok {
		t.Fatalf("内置注册表里应有 %s", id)
	}
	return sp
}

// createModel 直接往库里插一条模型行（**绕开写面**）。
//
// 写面（`CreateModel`/`UpdateModel`/`DeleteModel`）已搬进 `service/svcmodel`（P4a 正文第五刀）：
// 这些用例要的是"清单里有一条"这个**前提**，不是写面的行为——写面的判据跟着实现走，
// 在 `internal/service/svcmodel/declared_test.go`。
//
// 仍借 `model.ModelColumns` 落库而不是手写 SQL：前提必须与产品落库的形态同源
// （列化之后"清单里有一条"这句话的含义就是这些列）。
func (r *modelRig) createModel(t *testing.T, providerID, modelID string, mutate ...func(*dtomodel.ModelCreateReq)) *provider.ModelItem {
	t.Helper()
	ctx := context.Background()
	key := "sk-test"
	baseURL := "https://api.deepseek.com/v1"
	if _, err := r.svc.planes.Providers.CreateProvider(ctx, provider.CreateRequest{
		ID: providerID, Protocol: "openai", BaseURL: &baseURL, APIKey: &key,
	}); err != nil && errCode(err) != "provider_exists" {
		t.Fatalf("CreateProvider(%s): %v", providerID, err)
	}
	req := dtomodel.ModelCreateReq{ProviderID: providerID, Model: modelID}
	for _, m := range mutate {
		m(&req)
	}
	id := objid.New(objid.Model)
	modalities := resource.ModalityText
	if req.InputModalities != nil {
		modalities = resource.ModalitiesToColumn(*req.InputModalities)
	}
	name, note := "", ""
	if req.Name != nil {
		name = *req.Name
	}
	if req.Note != nil {
		note = *req.Note
	}
	ok, err := r.svc.planes.Resources.Store().CreateModelDefinition(ctx, model.ModelColumns{
		ID: id, ProviderID: providerID, Model: modelID, Name: name, Note: note,
		InputModalities: modalities, MaxInputTokens: req.MaxInputTokens, MaxOutputTokens: req.MaxOutputTokens,
	}, defn.DefinitionWriteActor)
	if err != nil || !ok {
		t.Fatalf("插模型行: ok=%v err=%v", ok, err)
	}
	if req.IsDefault != nil && *req.IsDefault {
		if err := r.svc.planes.Resources.Store().SetDefaultModelID(ctx, id); err != nil {
			t.Fatalf("置默认: %v", err)
		}
	}
	resp, found, err := r.svc.Planes().Providers.ModelRow(ctx, id)
	if err != nil || !found {
		t.Fatalf("读回刚插的行: found=%v err=%v", found, err)
	}
	return resp
}

// TestModelValueForTwoAlgorithmsAgree 下发（T2）与交付（T5）**共用同一个生成规则**，
// 而那条规则由**各底座自己声明**（注册表第四张表），内核只做分发。
//
// 判据是"同一输入产出逐字节相同的值"——本测试用两个**形态不同**的实现把"内核没有内建形态"
// 这件事钉住：
//
//	dsh（真实 adapter，走 `pkg/runtime/dsh`）：`["mate-<provider>","<model>"]`（JSON 数组；第一段是
//	派生路由键，v25 起带平台保留前缀）
//	rtx（合成夹具，走 internal/testutil/runtimetest）：`<provider>/<model>`
//
// 两者形态**刻意不同**：如果内核还留着一个 `switch runtimeID`，它就必然挑中其中一个形态、
// 让另一条断言红。清单里**不存** value：它只存生成 value 的输入（这正是本方案最容易写错的一处：
// 把运行时给的 value 存回清单就是把"事实"当"意图"）。
func TestModelValueForTwoAlgorithmsAgree(t *testing.T) {
	builtin := dsh.Registry()
	v, err := provider.ModelValueFor(builtin, runtime.DefaultBuildRuntime, "deepseek-main", "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	// v25 起第一段是**派生后的路由键**（带平台保留前缀）——它与配置里声明的键必须逐字同源。
	if v != `["mate-deepseek-main","deepseek-v4-flash"]` {
		t.Fatalf("内置底座的模型值形态不符: %q", v)
	}
	var arr []string
	if err := json.Unmarshal([]byte(v), &arr); err != nil || len(arr) != 2 {
		t.Fatalf("内置底座的值应是合法 JSON 数组: %q err=%v", v, err)
	}
	// 合成底座的形态来自**它自己登记的实现**（内核从未听说过 `<provider>/<model>`）。
	synthetic, _ := runtimetest.NewRegistry(runtime.LaunchSpec{})
	v, err = provider.ModelValueFor(synthetic, runtimetest.ID, "deepseek-main", "deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if v != "deepseek-main/deepseek-v4-flash" {
		t.Fatalf("合成底座的模型值形态不符: %q", v)
	}
	// 两个形态**必须不同**：相同的话，"内核按 id 挑形态"这条失效方式就测不出来了。
	builtinV, _ := provider.ModelValueFor(builtin, runtime.DefaultBuildRuntime, "p", "m")
	syntheticV, _ := provider.ModelValueFor(synthetic, runtimetest.ID, "p", "m")
	if builtinV == syntheticV {
		t.Fatalf("两条形态退化成同一个（%q）：本判据失去区分度，内核里的 id 特判会假绿", builtinV)
	}
	if _, err := provider.ModelValueFor(builtin, "some-third-runtime", "p", "m"); errCode(err) != "model_value_unsupported" {
		t.Fatalf("未登记形态的 runtime 应拒绝猜模型值形状，got %q", errCode(err))
	}
	// 行里不得出现**生成的值**（平台只存输入：provider_id + model_id）。
	r := newModelRig(t)
	resp := r.createModel(t, "p1", "v4-flash")
	row, found, err := r.svc.planes.Resources.Store().GetModelDefinition(context.Background(), resp.ID)
	if err != nil || !found {
		t.Fatalf("读行: found=%v err=%v", found, err)
	}
	if row.Model != "v4-flash" || row.ProviderID != "p1" {
		t.Fatalf("身份列应逐字落库: %+v", row)
	}
	// v13 列化之后"生成的值"连落点都没有：整行字段是列，没有自由 JSON 可塞。
	if row.Name != "" || row.Note != "" || row.InputModalities != "text" {
		t.Fatalf("内容列应只有平台拥有的字段（name/note/input_modalities）: %+v", row)
	}
}

// TestModelListReadsEmptyWithoutSQLite memory 模式（无清单载体）：读面回空列表。
//
// 写面（"没有载体就 40501"）随实现在 `internal/service/svcmodel/declared_test.go`。
func TestModelListReadsEmptyWithoutSQLite(t *testing.T) {
	ctx := context.Background()
	svc := NewService(dsh.Registry(), eventbus.New())
	items, err := svc.planes.Providers.ListModels(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("memory 模式的清单读面应回空列表: %+v err=%v", items, err)
	}
}

// TestResolveDefaultModelFallbackChain：回落链三条断言。
//
//	① 无默认 → 空串（**不下发**）；
//	② 有默认 → DefaultModelValueFor 给出该 runtime 认得的值（T2 确实会下发）；
//	③ 未声明模型值形态的 runtime → 空串（不猜形状）。
func TestResolveDefaultModelFallbackChain(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	v, err := r.svc.planes.Providers.DefaultModelValueFor(ctx, runtimetest.ID)
	if err != nil || v != "" {
		t.Fatalf("无默认应回空串（不下发）: %q err=%v", v, err)
	}
	r.createModel(t, "p1", "m-a", func(req *dtomodel.ModelCreateReq) { req.IsDefault = ptr(true) })
	v, err = r.svc.planes.Providers.DefaultModelValueFor(ctx, runtimetest.ID)
	if err != nil || v != "p1/m-a" {
		t.Fatalf("有默认应回生成的值: %q err=%v", v, err)
	}
	// dsh 的形态是 JSON 数组（同一个平台模型 id，两种底座各自的词汇）。
	if _, err := r.svc.planes.Providers.DefaultModelValueFor(ctx, "dsh"); err != nil {
		t.Fatalf("dsh 也该能算出它的值: %v", err)
	}
	if v, err := r.svc.planes.Providers.DefaultModelValueFor(ctx, "stub"); err != nil || v != "" {
		t.Fatalf("未声明模型值形态的 runtime 应回空串: %q err=%v", v, err)
	}
}

// TestModelListProviderDeleteGuard 供应商被模型挂靠时删除被拒（两类引用都要交出来）。
func TestModelListProviderDeleteGuard(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	_ = r.createModel(t, "p1", "m-a")
	_, err := r.svc.planes.Providers.DeleteProvider(ctx, "p1")
	if errCode(err) != "provider_in_use" {
		t.Fatalf("被模型挂靠的供应商应 provider_in_use，got %q（%v）", errCode(err), err)
	}
	if !strings.Contains(err.Error(), "模型条目") {
		t.Fatalf("文案应交出模型条目这一类引用: %v", err)
	}
}

// TestDefaultModelReasoningEffortNotSent：本版**不下发** effort（模型与努力级别在 ACP 上是
// 两个独立 option，平台首版只管 category=model）。这条钉住"没有偷偷多发一个 option"。
func TestDefaultModelReasoningEffortNotSent(t *testing.T) {
	var mc runtime.ModelConfig
	b, err := json.Marshal(mc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "effort") {
		t.Fatalf("T2 的载荷里不该有 effort（本版只管 category=model）: %s", b)
	}
	// dsh 必须**声明凭据交付面**（原 v11 断言的是"已声明模型配置落点"，v12 的那个概念换成
	// 落点 + 凭据两条声明；这里断言后者，因为它是新链路的关键）。
	reg := dsh.Registry()
	sp, ok := reg.Get(runtime.DefaultBuildRuntime)
	if !ok {
		t.Fatal("dsh 应已注册")
	}
	if sp.Credential.RefPattern == "" {
		t.Fatal("dsh 应已声明凭据引用名模板（逐供应商唯一的那个）")
	}
	_ = testutil.StubRegistry() // 保持 testutil 依赖（夹具在别处复用）
}
