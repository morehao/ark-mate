package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P4a 正文第五刀「平台模型清单写面归属」守卫 ———
//
// 平台模型清单的**写面**（`CreateModel`/`UpdateModel`/`DeleteModel`）从 `manager/model_list.go`
// （`*Service` 的方法）搬进 `internal/app/model`，形状是同一个模板：
//
//	`model.Declared`（用例：校验 / 错误码出口 / 单一默认 / 行 CAS / 响应装配）
//	  ← 两个**依赖事实**（`*store.Store` 清单载体、`*provider.Plane` 挂靠校验）
//	  + 一个**过渡端口** `model.Host`（读一条的投影 / 生效面回执 / 同步触发——这三条还在
//	    `manager`，随读面与同步面在 P4b 第二刀 / P5 搬进来）
//	      ← 装配根（`internal/bootstrap/assembly`）把三者接上并**造出**用例（`model.NewDeclared(...)`），
//	        控制器只收接口（v3.1 起实例只在装配根造）
//
// 为什么是"一个端口"而不是把写面也留给 `manager`：写面的判据（(provider_id, model_id) 唯一、
// 默认是单一值、删默认要如实回带 default_cleared、memory 模式 40501）是**用例**的判据，
// 而读面那半边要**执行面**（活连接的 session 投影、spawn 采样），它随 `Orchestrator` 在 P5
// 一起 dissolve——于是这一刀按**面**切，不按领域切（与同包的档案面/自检面同一条口径）。
//
// 三种失效各有一条判据：
//
//	① **实现回流**：谁在 `manager` 里再写一遍校验、再铸一次错误码、再判一次单一默认——两份
//	   实现静默分叉（一处允许多个默认、另一处只留一个；一处写 `model_exists`、另一处 40501），
//	   而两边都编译得过、都"看起来在工作"；
//	② **接口面不清**：写面搬走了而 `ModelSvc` 上还留着那三条，于是端口与实际实现各说一份，
//	   "这一段还剩什么"再也看不出来；
//	③ **装配漏接**：装配根忘了 `model.NewDeclared(...)`（写面 nil ⇒ 一提交就 panic）
//	   或 `app.go` 没交 `Store`（⇒ memory 模式的 40501 变成"所有模式都 40501"，而读面照常绿）。
//
// 反证（实跑）：把 `func (s *Service) CreateModel(` 加回 `manager`（真实现的形态，能编译）→ 红；
// 抽掉装配根的 `model.NewDeclared(` → 红；把 `CreateModel` 加回 `ModelSvc` 接口 → 红；
// 把 `app.go` 的 `Store: st` 抽掉 → 红；把 `router.Ports` 的 `ModelHost` 字段换回
// `model.DeclaredSvc` → 红。

// declaredUseCaseAnchors 写面用例层的实现锚（**实现**，不是转发）。
var declaredUseCaseAnchors = []string{
	"func NewDeclared(",
	"var _ DeclaredSvc = (*Declared)(nil)",
	"func (m *Declared) CreateModel(",
	"func (m *Declared) UpdateModel(",
	"func (m *Declared) DeleteModel(",
	`"model_list_unavailable"`,
	`"model_exists"`,
	`"model_not_found"`,
	`"provider_not_found"`,
	`"modality_unsupported"`,
}

// declaredPortAnchors 过渡端口 `Host` 的三行签名必须真的在（写面靠它们取"还没搬完的那半边"）。
var declaredPortAnchors = []string{
	"type Host interface {",
	"ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error)",
	"ModelEffect(defaultTouched bool) *dtomodel.ModelEffect",
	"TrySyncModels(ctx context.Context) string",
}

// declaredRelocationMarkers 写面实现回流到 `manager` 的痕迹（校验、错误码、独占辅助）。
var declaredRelocationMarkers = []string{
	"func (s *Service) CreateModel(",
	"func (s *Service) UpdateModel(",
	"func (s *Service) DeleteModel(",
	"modelListUnavailableErr(",
	"rejectRuntimeField(",
	"normalizeInputModalities(",
	"normalizeCapacity(",
	"modelExistsErr(",
	"modelNotFoundErr(",
}

// managerModelPortAnchors 还留在 `manager` 的那半边：过渡端口的三条实现必须真的在
// （它们不在 = `model.Host` 没人满足，而"改名 + 顺手改 app"能编译过）。
//
// P4b 第二刀（读面 + 同步面进 `svcmodel`）会把这三条逐条删掉，**判据跟着少**——
// 这正是"过渡期债务单调收缩"应有的形状。
var managerModelPortAnchors = []string{
	"func (s *Service) ModelEffect(",
}

// registryModelPortAnchors P5a 起同步面在 `registry.Resources`（P4b/取样面搬完后与读面合流）。
var registryModelPortAnchors = []string{
	"func (r *Resources) TrySyncModels(",
}

// TestModelWriteUseCaseLivesInModelSvc 见文件头。
func TestModelWriteUseCaseLivesInModelSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "app", "model")

	declaredSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "declared.go")))
	for _, a := range append(append([]string{}, declaredUseCaseAnchors...), declaredPortAnchors...) {
		if !strings.Contains(declaredSrc, a) {
			t.Errorf("internal/app/model/declared.go 里找不到 %q：写面用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}
	// 清单载体是**依赖事实**（写面要落库），而实现层不许认识：`manager` 是过渡期的邻居，
	// 用例层通过端口取它的东西，绝不 import 它（否则"谁拥有这段逻辑"就没有答案了）。
	if strings.Contains(declaredSrc, `"github.com/morehao/ark-mate/internal/app/run"`) {
		t.Error("internal/app/model 里出现 `internal/app/run` 的 import：写面通过 `Host` 端口取那半边，不 import 实现层。")
	}
	// v3.1 P4：用例层**不再 import store**，改吃装配根交下来的窄端口（`ModelStore`）。
	// 判据双向：接口必须在本包（否则端口又跑到实现层去了）；store 不得再被 import。
	if !strings.Contains(declaredSrc, "type ModelStore interface {") {
		t.Error("internal/app/model 里找不到 `type ModelStore interface {`：清单载体的窄端口必须在用例层声明（v3.1 P4）。")
	}
	if strings.Contains(declaredSrc, `"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"`) {
		t.Error("internal/app/model 又 import 了 `internal/adapters/outbound/sqlite/store`：清单载体走 `ModelStore` 端口，" +
			"`internal/app` 的层表 deny 已把这条边钉死。")
	}

	// ② 接口面切分：写面在 `DeclaredSvc` 上，读/目录/同步在 `ModelSvc` 上，两边不许互相夹带。
	modelSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "model.go")))
	declaredIf := interfaceBody(t, modelSrc, "DeclaredSvc")
	for _, m := range []string{"CreateModel(", "UpdateModel(", "DeleteModel("} {
		if !strings.Contains(declaredIf, m) {
			t.Errorf("`DeclaredSvc` 接口里找不到 %q：写面的接口面被搬走了？", m)
		}
	}
	for _, m := range []string{"ModelsView(", "ModelCatalog(", "SyncModels(", "ListModels("} {
		if strings.Contains(declaredIf, m) {
			t.Errorf("`DeclaredSvc` 接口里出现 %q：它只声明写面三条——读面与目录面各有自己的形态"+
				"（读面今天仍在 `manager`，目录面要执行面），混进写面接口就等于把它们重新绑在一份实现上。", m)
		}
	}
	modelIf := interfaceBody(t, modelSrc, "ModelSvc")
	for _, m := range []string{"ModelsView(", "ModelCatalog(", "SyncModels("} {
		if !strings.Contains(modelIf, m) {
			t.Errorf("`ModelSvc` 接口里找不到 %q：还没搬完的那半边被抽掉了？", m)
		}
	}
	for _, m := range []string{"CreateModel(", "UpdateModel(", "DeleteModel("} {
		if strings.Contains(modelIf, m) {
			t.Errorf("`ModelSvc` 接口里仍声明 %q：写面已搬进 `model.Declared`（P4a 正文第五刀），"+
				"接口上留着它等于让端口与实际实现各说一份，「这一段还剩什么」就看不出来了。", m)
		}
	}
	if strings.Contains(modelIf, "ListModels(") {
		t.Error("`ModelSvc` 接口里出现 `ListModels(`：它的实现（与投影）今天仍在 `manager`，" +
			"接口上声明一个没人接的实现只会让装配点编译不过——读面搬过来的那一天再声明它。")
	}

	// ① manager 侧：不得再有实现痕迹；③ 还留在那里的端口实现必须真的在。
	mgr := filepath.Join(root, "internal", "app", "run")
	files := productionGoFiles(t, mgr)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	var mgrBody strings.Builder
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		mgrBody.WriteString(body)
		for _, m := range declaredRelocationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/app/run/%s 里出现模型写面的实现痕迹 %q：它**只有一份实现**，"+
					"在 `internal/app/model`（P4a 正文第五刀）。\n"+
					"manager 交出的是清单载体与供应商域平面（依赖事实）+ 过渡端口的三条实现。\n"+
					"在这里再写一遍的后果是校验与错误码静默分叉（一处允许多个默认、另一处只留一个），"+
					"而两边都编译得过。", filepath.Base(path), m)
			}
		}
	}
	for _, a := range managerModelPortAnchors {
		if !strings.Contains(mgrBody.String(), a) {
			t.Errorf("internal/app/run 的生产文件里找不到 %q：`model.Host` 的这条实现被改名/删掉了？"+
				"（改名后 `app.go` 一起改就能编译过，而端口的那件事没人做）", a)
		}
	}
	// ModelRow 随模型读面进了 `domain/provider`；`registry.ModelViews` 嵌入该平面后一并满足
	// `model.Host`。判据落在**真实现**上（不是转发）。
	providerModels := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "provider", "models.go")))
	if !strings.Contains(providerModels, "func (pl *Plane) ModelRow(") {
		t.Errorf("`internal/domain/provider/models.go` 里找不到 `func (pl *Plane) ModelRow(`：" +
			"`model.Host` 的读一条实现必须有真落点")
	}
	views := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "outbound", "registry", "models_view.go")))
	if !strings.Contains(views, "*provider.Plane") {
		t.Errorf("`internal/adapters/outbound/registry/models_view.go` 的 ModelViews 没有嵌入 `*provider.Plane`：" +
			"ModelSvc/Host 的读面就少一份真实现")
	}
	regBody := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "outbound", "registry", "resources.go")))
	for _, a := range registryModelPortAnchors {
		if !strings.Contains(regBody, a) {
			t.Errorf("internal/adapters/outbound/registry/resources.go 里找不到 %q：同步面的端口实现被改名/删掉了？", a)
		}
	}

	// ③ 装配：用例在**装配根**造（`app` 与 `router` 都不许引用 `service/svc*`）。
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrmodel", "models.go")))
	for _, m := range []string{"h.declared.CreateModel(", "h.declared.UpdateModel(", "h.declared.DeleteModel("} {
		if !strings.Contains(ctr, m) {
			t.Errorf("internal/adapters/inbound/http/controller/ctrmodel/models.go 里找不到 %q：三个写动作必须走用例出口。", m)
		}
	}
	for _, m := range []string{"h.svc.CreateModel(", "h.svc.UpdateModel(", "h.svc.DeleteModel("} {
		if strings.Contains(ctr, m) {
			t.Errorf("internal/adapters/inbound/http/controller/ctrmodel/models.go 里仍出现 %q：写面已不在 `ModelSvc` 上——"+
				"这两条路都能编译的那一天，就是「实现收了但出口没换」的那一天。", m)
		}
	}

	// v3.1：装配根造用例；控制器只收接口。
	asm := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "services.go")))
	for _, want := range []string{"model.NewDeclared(", "Model:         modelFace"} {
		if !strings.Contains(asm, want) {
			t.Errorf("`internal/bootstrap/assembly/services.go` 里找不到 %q：模型写面的依赖事实与用例实例都在装配根交出。", want)
		}
	}
	ctrSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrmodel", "models.go")))
	if strings.Contains(ctrSrc, "model.NewDeclared(") {
		t.Error("`ctrmodel` 里又出现 `model.NewDeclared(`：用例实例只在装配根造，控制器只收接口。")
	}
}

// interfaceBody 取 `type <name> interface {` 到顶格 `}` 之间的正文。
//
// fail-closed：锚找不到、或找不到收尾的顶格 `}`，一律 Fatal——判据失锚的最坏表现是
// "守卫照样绿、只是不再检查任何东西"（与 `uiSrcRoots()` 那条兜底同形）。
func interfaceBody(t *testing.T, src, name string) string {
	t.Helper()
	marker := "type " + name + " interface {"
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("接口 %s 的声明找不到（判据失锚）", name)
	}
	rest := src[i+len(marker):]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("接口 %s 的收尾 `}` 找不到（判据失锚）", name)
	}
	body := rest[:j]
	if strings.TrimSpace(body) == "" {
		t.Fatalf("接口 %s 的正文是空的（判据失锚）", name)
	}
	return body
}
