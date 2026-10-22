package sessionruntime

import (
	"context"
	"regexp"
	"testing"
	"time"

	defn "github.com/morehao/ark-mate/internal/core/definition"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/core/resource"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— v26：会话模型引用 = **平台模型行 id**（定案「辰」）———

// platformModelRefForm 平台模型行 id 的形状（判据的形态那一半）。
var platformModelRefForm = regexp.MustCompile(`^mdl_[0-9A-HJKMNP-TV-Z]{26}$`)

// sessionModelRig 一个"能建会话 + 有平台清单 + 有模型值形态"的架子。
//
// 在 `newGenRig`（stub ACP + 真库 + 构建期底座 = stub）之上补一件事：给 stub 登记一条模型值形态。
// stub 不是内置 adapter，所以 `RegisterBuiltinPorts` 不会替它登记那第四张表——不补的话
// `modelValueFor` 一律报 `model_value_unsupported`，而那样测出来的只是"没有模型语义"。
func sessionModelRig(t *testing.T) *genRig {
	t.Helper()
	r := newGenRig(t)
	// 合成形态（`<provider>/<model>`）——与 `runtimetest.Register` 给 rtx 登记的是同一个实现。
	r.svc.planes.Registry.RegisterModelValue(r.runtme, runtimetestModelValue{})
	return r
}

// runtimetestModelValue 本文件的模型值形态：`<provider>/<model>`（与 `internal/testutil/runtimetest`
// 的合成底座同形；这里不复用那个类型是为了让**多命中**那几个用例能换成自己的形态）。
type runtimetestModelValue struct{}

func (runtimetestModelValue) Value(providerID, model string) (string, error) {
	return providerID + "/" + model, nil
}

// byModelNameOnly 一个"只用模型名"的值形态（**只为构造多命中**）。
//
// 两家供应商下的同名上游模型会派生出**同一个**运行时值——那时这个值指不到确定的一行，
// 回填与目录注解都必须 fail-closed（宁可清掉/不可选，也不能挑一条 = 静默选错模型）。
type byModelNameOnly struct{}

func (byModelNameOnly) Value(_, model string) (string, error) { return "m:" + model, nil }

// createProviderAndModel 造一条"供应商 + 模型行"，返回模型行。
//
// 模型行**直接插库**（写面已搬进 `service/svcmodel`：这些用例测的是会话侧怎么解析模型，
// 不是写面，见 `seedModelRow`）。
func createProviderAndModel(t *testing.T, svc *Service, providerID, modelName string) *provider.ModelItem {
	t.Helper()
	ctx := context.Background()
	key, baseURL := "sk-test", "https://api.example.com/v1"
	if _, err := svc.planes.Providers.CreateProvider(ctx, provider.CreateRequest{
		ID: providerID, Protocol: "openai", BaseURL: &baseURL, APIKey: &key,
	}); err != nil && errCode(err) != "provider_exists" {
		t.Fatalf("CreateProvider(%s): %v", providerID, err)
	}
	id := objid.New(objid.Model)
	ok, err := svc.planes.Resources.Store().CreateModelDefinition(ctx, model.ModelColumns{
		ID: id, ProviderID: providerID, Model: modelName, InputModalities: resource.ModalityText,
	}, defn.DefinitionWriteActor)
	if err != nil || !ok {
		t.Fatalf("插模型行(%s/%s): ok=%v err=%v", providerID, modelName, ok, err)
	}
	row, found, err := svc.Planes().Providers.ModelRow(ctx, id)
	if err != nil || !found {
		t.Fatalf("读回模型行 %s: found=%v err=%v", id, found, err)
	}
	return row
}

// TestResolveSessionModelRejectsUnknown 守卫⑧的行为面：**对不上平台清单的入参一律被拒**，
// 绝不静默回落。
//
// 三种入参各对应一种真实的错法：
//
//	① 形状不对（旧的**不透明值**——升级后残留的本地选择、或前端还在发运行时目录的 value）；
//	② 形状对但清单里没有（模型行被删了 / 是别的机器上建的 id）；
//	③ 空串是**合法**的（= 用平台默认那一行），拿它当"错误"会把"跟随默认"这条正常路径堵死。
//
// 反证（实跑）：把拒绝分支改成"回落到默认行" → ①② 两段的断言红。
func TestResolveSessionModelRejectsUnknown(t *testing.T) {
	r := sessionModelRig(t)
	ctx := context.Background()
	row := createProviderAndModel(t, r.svc, "p1", "m-1")

	// ① 旧形态（运行时目录的不透明值）：形状就不对。
	_, _, err := r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, `["p1","m-1"]`)
	if errCode(err) != "model_not_found" || numericCode(err) != 40401 {
		t.Fatalf("旧形态的不透明值应 40401 model_not_found，got %q/%d（%v）", errCode(err), numericCode(err), err)
	}

	// ② 形状对但查不到行。
	ghost := objid.New(objid.Model)
	if _, _, err := r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, ghost); errCode(err) != "model_not_found" {
		t.Fatalf("清单里没有的 %s 应 model_not_found，got %q（%v）", ghost, errCode(err), err)
	}

	// ③ 形状对且在清单里 → 返回**平台行 id** 与**派生值**（两层词汇各归各的）。
	id, cfg, err := r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, row.ID)
	if err != nil {
		t.Fatalf("清单里的模型行应解析成功: %v", err)
	}
	if id != row.ID {
		t.Fatalf("返回的平台行 id 应是 %q，实际 %q", row.ID, id)
	}
	if cfg.Model != "p1/m-1" {
		t.Fatalf("运行时值应由 adapter 现算（stub 形态 `<provider>/<model>`），实际 %q", cfg.Model)
	}
	// `Provider` 是**留痕语义**：此前填的是恒空的 `session.SessionRec.ProviderID`，现在由模型行派生。
	if cfg.Provider != "p1" {
		t.Fatalf("ModelConfig.Provider 应由模型行派生（留痕语义），实际 %q", cfg.Provider)
	}

	// ④ 空串 = 用默认那一行；没有默认 → 不下发（不是错误）。
	if _, cfg, err := r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, ""); err != nil || cfg.Model != "" {
		t.Fatalf("未设默认模型时应回「不下发」而不是报错: cfg=%+v err=%v", cfg, err)
	}
	if err := r.svc.planes.Resources.Store().SetDefaultModelID(ctx, row.ID); err != nil {
		t.Fatalf("设默认模型: %v", err)
	}
	id, cfg, err = r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, "")
	if err != nil || id != row.ID || cfg.Model != "p1/m-1" {
		t.Fatalf("设了默认之后空串应解析成那一行: id=%q cfg=%+v err=%v", id, cfg, err)
	}
}

// TestUpdateSessionModelStoresPlatformID 守卫⑥的行为面：**任何写路径落库的取值恒满足
// `空 或 ^mdl_<26>$`**，且解析失败时一个字节都不写。
//
// 走的是 `Service.UpdateSession`（会话元数据写面）：会话不在活跃集合时它只落元数据，
// 正好把"入参 → 落库值"这条链单独拎出来验（不需要能跑 set_config_option 的底座）。
func TestUpdateSessionModelStoresPlatformID(t *testing.T) {
	r := sessionModelRig(t)
	ctx := context.Background()
	row := createProviderAndModel(t, r.svc, "p1", "m-1")

	// 会话记录直接登记（不经 ACP）：`UpdateSession` 找不到活句柄时会走"只更新元数据"那一支。
	seed := func(id string) {
		r.svc.sessionPlane.RegisterSession(&session.SessionRec{ID: id, Cwd: t.TempDir(), RuntimeID: r.runtme,
			Status: session.SessionStatusInProgress})
	}
	readModel := func(id string) string {
		got, ok, err := r.svc.planes.Resources.Store().GetSession(ctx, id)
		if err != nil || !ok {
			t.Fatalf("读回会话 %s: ok=%v err=%v", id, ok, err)
		}
		return got.ModelID
	}

	// ① 合法平台行 id → 库里就是它（不是运行时值）。
	const okID = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2Q1"
	seed(okID)
	if _, err := metaSvc(r.svc).UpdateSession(ctx, okID, dtosession.SessionPatch{Model: ptr(row.ID)}); err != nil {
		t.Fatalf("切换到一个清单里的模型应成功: %v", err)
	}
	if got := readModel(okID); got != row.ID {
		t.Fatalf("落库的应是**平台行 id** %q，实际 %q（旧形态：运行时的不透明值）", row.ID, got)
	}
	if !platformModelRefForm.MatchString(readModel(okID)) {
		t.Fatalf("落库取值必须满足 `^mdl_<26 位>`：%q", readModel(okID))
	}

	// ② 旧形态的不透明值 → 40401，且**一个字节都不写**（库里仍是上一次的值）。
	const badID = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2Q2"
	seed(badID)
	before := readModel(badID)
	if _, err := metaSvc(r.svc).UpdateSession(ctx, badID, dtosession.SessionPatch{Model: ptr("p1/m-1")}); numericCode(err) != 40401 {
		t.Fatalf("运行时值不应被接受（它是**另一个词**）：应 40401 model_not_found，got %d（%v）",
			numericCode(err), err)
	}
	if got := readModel(badID); got != before {
		t.Fatalf("解析失败时不得落库：before=%q after=%q", before, got)
	}

	// ③ 清单里没有的 id → 同样被拒且不落库。
	if _, err := metaSvc(r.svc).UpdateSession(ctx, badID, dtosession.SessionPatch{Model: ptr(objid.New(objid.Model))}); numericCode(err) != 40401 {
		t.Fatalf("查不到的模型行应 40401 model_not_found，got %d", numericCode(err))
	}
	if got := readModel(badID); got != before {
		t.Fatalf("查不到行时不得落库：before=%q after=%q", before, got)
	}
}

// TestBackfillSessionModelRefs 守卫⑥/⑧的**存量那半**：旧形态的不透明值 → 平台行 id。
//
// 四类会话各对应一种必须区分的结局：
//
//	(a) 唯一命中        → 写平台行 id；
//	(b) 零命中          → 置空串（`sessions.model_id` 是 NOT NULL DEFAULT ''，"置空"就是空串）
//	                      并在日志计数里出现；
//	(c) 已经是平台 id   → **不动**（幂等的关键：把上一次跑到一半的产物当"旧形态"清掉 = 把成功
//	                      当成失败处置）；
//	(d) 多命中          → 置空串（fail-closed：这个值指不到确定的一行）。
//
// 全部成功才清标记；任何一步失败标记留着（这里只验成功路径与"标记被清"，失败路径由
// `rebuildStaleReadModels` 的同形实现与代码 review 兜）。
func TestBackfillSessionModelRefs(t *testing.T) {
	r := sessionModelRig(t)
	ctx := context.Background()
	row := createProviderAndModel(t, r.svc, "p1", "m-1")

	// (d) 多命中：换成"只用模型名"的值形态，于是两家供应商下的同名模型派生出同一个值。
	row2 := createProviderAndModel(t, r.svc, "p2", "m-1")

	seed := func(id, runtimeID, modelID string) {
		if err := r.svc.planes.Resources.Store().UpsertSession(ctx, model.SessionEntity{
			ID: id, Cwd: "/tmp/w", WorkspaceTemporary: false,
			ModelID: modelID, RuntimeID: runtimeID, Status: "completed",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("写会话夹具 %s: %v", id, err)
		}
	}
	read := func(id string) string {
		got, ok, err := r.svc.planes.Resources.Store().GetSession(ctx, id)
		if err != nil || !ok {
			t.Fatalf("读回会话 %s: ok=%v err=%v", id, ok, err)
		}
		return got.ModelID
	}

	// 先用合成形态的派生值造 (a)/(b)/(c)。
	const ( // 形状与生产一致：`ses_` + 26 位 Crockford
		sesA = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QA"
		sesB = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QB"
		sesC = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QC"
		sesD = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QD"
	)
	seed(sesA, r.runtme, "p1/"+row.Model) // (a) 唯一命中
	seed(sesB, r.runtme, "p9/nope")       // (b) 零命中
	seed(sesC, r.runtme, row2.ID)         // (c) 已经是平台 id

	if err := r.svc.planes.Resources.Store().SetMeta(ctx, model.MetaKeySessionsModelRefBackfill, "26"); err != nil {
		t.Fatalf("置回填标记: %v", err)
	}
	r.svc.sessionPlane.BackfillSessionModelRefs(ctx)

	if got := read(sesA); got != row.ID {
		t.Fatalf("(a) 唯一命中应写成平台行 id %q，实际 %q", row.ID, got)
	}
	if got := read(sesB); got != "" {
		t.Fatalf("(b) 零命中应置空（列是 NOT NULL DEFAULT ''），实际 %q", got)
	}
	if got := read(sesC); got != row2.ID {
		t.Fatalf("(c) 已经是平台 id 的行不许被改写：want %q，got %q", row2.ID, got)
	}
	if _, ok, err := r.svc.planes.Resources.Store().GetMeta(ctx, model.MetaKeySessionsModelRefBackfill); err != nil || ok {
		t.Fatalf("全部成功后应清掉回填标记: ok=%v err=%v", ok, err)
	}

	// (d) 多命中：换形态之后，两家供应商下的 `m-1` 派生出同一个值。
	r.svc.planes.Registry.RegisterModelValue(r.runtme, byModelNameOnly{})
	seed(sesD, r.runtme, "m:m-1")
	if err := r.svc.planes.Resources.Store().SetMeta(ctx, model.MetaKeySessionsModelRefBackfill, "26"); err != nil {
		t.Fatalf("再置回填标记: %v", err)
	}
	r.svc.sessionPlane.BackfillSessionModelRefs(ctx)
	if got := read(sesD); got != "" {
		t.Fatalf("(d) 多命中必须 fail-closed（置空），实际 %q（随便挑一条 = 静默选错模型）", got)
	}
}

// TestCatalogAnnotatesCandidatesWithPlatformRowID 守卫⑧的另一半：目录候选带**平台行注解**，
// 没声明过的候选**不带**（= 客户端选不了它）。
//
// 为什么注解跟着候选走：会话的 `model` 收平台行 id，而选择发生在运行时目录上——一次取数就能
// "选运行时值、提交平台 id"。没有这个注解，前端只能把所有候选标成「平台未声明」。
func TestCatalogAnnotatesCandidatesWithPlatformRowID(t *testing.T) {
	r := sessionModelRig(t)
	ctx := context.Background()
	row := createProviderAndModel(t, r.svc, "p1", "m-1")

	// 目录里有两条候选：一条对得上平台行（`p1/m-1`），一条底座广告了但平台没声明。
	opts := []protocol.ConfigOption{{
		ID: "model", Name: "Model", Category: protocol.CategoryModel, Type: protocol.OptionTypeSelect,
		CurrentValue: "p1/m-1",
		Values: []protocol.ConfigOptionValue{
			{Value: "p1/m-1", Name: "已声明"},
			{Value: "p2/ghost", Name: "未声明"},
		},
	}}
	r.orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c1", Runtime: r.runtme, Session: &stubSession{options: opts}}))

	resp, err := r.svc.Planes().Providers.ModelCatalog(ctx)
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if len(resp.Options) != 1 || len(resp.Options[0].Values) != 2 {
		t.Fatalf("目录候选数不符: %+v", resp.Options)
	}
	vals := resp.Options[0].Values
	if vals[0].ModelID != row.ID {
		t.Fatalf("对得上平台行的候选应带上那一行的 id %q，实际 %q", row.ID, vals[0].ModelID)
	}
	if vals[1].ModelID != "" {
		t.Fatalf("平台没声明过的候选不得有注解（= 客户端选不了它），实际 %q", vals[1].ModelID)
	}
	// 反向：注解**不是** `Unavailable`（那个字段的含义是「底座拒过」，是另一件事）。
	if vals[1].Unavailable {
		t.Fatal("「平台未声明」不得借用 `unavailable`（那是「底座拒过这个值」的动态事实）")
	}
}

// TestSessionModelValueAgreesWithCatalogAnnotation 交叉判据：目录注解认出的行、回填认出的行、
// 会话写面接受的行，必须是**同一行**——三处各自算一遍派生值，任何一处口径漂了都会在这里露出来。
func TestSessionModelValueAgreesWithCatalogAnnotation(t *testing.T) {
	r := sessionModelRig(t)
	ctx := context.Background()
	row := createProviderAndModel(t, r.svc, "p1", "m-1")

	// 目录注解算出来的值 → 行。
	table := r.svc.Planes().Providers.ModelIDByValue(ctx, r.runtme)
	if len(table) != 1 {
		t.Fatalf("注解表应恰好一条（一条平台行），实际 %d 条: %v", len(table), table)
	}
	value := "p1/m-1"
	if table[value] != row.ID {
		t.Fatalf("注解表应把 %q 映到 %q，实际 %q", value, row.ID, table[value])
	}
	// 回填算出来的表 → 同一个映射。
	if got := r.svc.sessionPlane.RuntimeValueToModelID(ctx, r.runtme)[value]; got != row.ID {
		t.Fatalf("回填的映射与目录注解不一致：want %q，got %q（两处各算一遍派生值，口径漂了就分叉）", row.ID, got)
	}
	// 写面接受的行 → 同一个 id 与同一个值。
	id, cfg, err := r.svc.planes.Providers.ResolveSessionModel(ctx, r.runtme, row.ID)
	if err != nil || id != row.ID || cfg.Model != value {
		t.Fatalf("会话写面的解析与另外两条链不一致: id=%q cfg=%+v err=%v", id, cfg, err)
	}
}
