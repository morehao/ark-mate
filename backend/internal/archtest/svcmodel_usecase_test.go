package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第五刀「平台模型清单写面归属」守卫 ———
//
// 平台模型清单的**写面**（`CreateModel`/`UpdateModel`/`DeleteModel`）从 `manager/model_list.go`
// （`*Service` 的方法）搬进 `internal/service/svcmodel`，形状是同一个模板：
//
//	`svcmodel.Declared`（用例：校验 / 错误码出口 / 单一默认 / 行 CAS / 响应装配）
//	  ← 两个**依赖事实**（`*store.Store` 清单载体、`*provider.Plane` 挂靠校验）
//	  + 一个**过渡端口** `svcmodel.Host`（读一条的投影 / 生效面回执 / 同步触发——这三条还在
//	    `manager`，随读面与同步面在 P4b 第二刀 / P5 搬进来）
//	      ← `ctrmodel` 把三者接上并**造出**用例（`NewModelCtr` 里 `svcmodel.NewDeclared(...)`）
//
// 为什么是"一个端口"而不是把写面也留给 `manager`：写面的判据（(provider_id, model_id) 唯一、
// 默认是单一值、删默认要如实回带 default_cleared、memory 模式 40501）是**用例**的判据，
// 而读面那半边要**执行面**（活连接的 session 投影、spawn 采样），它随 `Orchestrator` 在 P5
// 一起 dissolve——于是这一刀按**面**切，不按领域切（与 `svcprovider` 的档案面/自检面同一条口径）。
//
// 三种失效各有一条判据：
//
//	① **实现回流**：谁在 `manager` 里再写一遍校验、再铸一次错误码、再判一次单一默认——两份
//	   实现静默分叉（一处允许多个默认、另一处只留一个；一处写 `model_exists`、另一处 40501），
//	   而两边都编译得过、都"看起来在工作"；
//	② **接口面不清**：写面搬走了而 `ModelSvc` 上还留着那三条，于是端口与实际实现各说一份，
//	   "这一段还剩什么"再也看不出来；
//	③ **装配漏接**：`ctrmodel` 忘了 `svcmodel.NewDeclared(...)`（写面 nil ⇒ 一提交就 panic）
//	   或 `app.go` 没交 `Store`（⇒ memory 模式的 40501 变成"所有模式都 40501"，而读面照常绿）。
//
// 反证（实跑）：把 `func (s *Service) CreateModel(` 加回 `manager`（真实现的形态，能编译）→ 红；
// 抽掉 `ctrmodel` 的 `svcmodel.NewDeclared(` → 红；把 `CreateModel` 加回 `ModelSvc` 接口 → 红；
// 把 `app.go` 的 `Store: st` 抽掉 → 红；把 `router.Ports` 的 `ModelHost` 字段换回
// `svcmodel.DeclaredSvc` → 红。

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
// （它们不在 = `svcmodel.Host` 没人满足，而"改名 + 顺手改 app"能编译过）。
//
// P4b 第二刀（读面 + 同步面进 `svcmodel`）会把这三条逐条删掉，**判据跟着少**——
// 这正是"过渡期债务单调收缩"应有的形状。
var managerModelPortAnchors = []string{
	"func (s *Service) ModelRow(",
	"func (s *Service) ModelEffect(",
	"func (s *Service) TrySyncModels(",
}

// TestModelWriteUseCaseLivesInModelSvc 见文件头。
func TestModelWriteUseCaseLivesInModelSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svcmodel")

	declaredSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "declared.go")))
	for _, a := range append(append([]string{}, declaredUseCaseAnchors...), declaredPortAnchors...) {
		if !strings.Contains(declaredSrc, a) {
			t.Errorf("internal/service/svcmodel/declared.go 里找不到 %q：写面用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}
	// 清单载体是**依赖事实**（写面要落库），而实现层不许认识：`manager` 是过渡期的邻居，
	// 用例层通过端口取它的东西，绝不 import 它（否则"谁拥有这段逻辑"就没有答案了）。
	if strings.Contains(declaredSrc, `"github.com/morehao/ark-mate/internal/manager"`) {
		t.Error("internal/service/svcmodel 里出现 `internal/manager` 的 import：写面通过 `Host` 端口取那半边，不 import 实现层。")
	}
	if !strings.Contains(declaredSrc, `"github.com/morehao/ark-mate/internal/store"`) {
		t.Error("internal/service/svcmodel 里没有 `internal/store` 的 import：清单载体是这一层的**依赖事实**（P4a 起装配点交出 store），" +
			"它被拿走意味着写面又回到了「只认 Service」的老形状。")
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
			t.Errorf("`ModelSvc` 接口里仍声明 %q：写面已搬进 `svcmodel.Declared`（P4a 正文第五刀），"+
				"接口上留着它等于让端口与实际实现各说一份，「这一段还剩什么」就看不出来了。", m)
		}
	}
	if strings.Contains(modelIf, "ListModels(") {
		t.Error("`ModelSvc` 接口里出现 `ListModels(`：它的实现（与投影）今天仍在 `manager`，" +
			"接口上声明一个没人接的实现只会让装配点编译不过——读面搬过来的那一天再声明它。")
	}

	// ① manager 侧：不得再有实现痕迹；③ 还留在那里的端口实现必须真的在。
	mgr := filepath.Join(root, "internal", "manager")
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
				t.Errorf("internal/manager/%s 里出现模型写面的实现痕迹 %q：它**只有一份实现**，"+
					"在 `internal/service/svcmodel`（P4a 正文第五刀）。\n"+
					"manager 交出的是清单载体与供应商域平面（依赖事实）+ 过渡端口的三条实现。\n"+
					"在这里再写一遍的后果是校验与错误码静默分叉（一处允许多个默认、另一处只留一个），"+
					"而两边都编译得过。", filepath.Base(path), m)
			}
		}
	}
	for _, a := range managerModelPortAnchors {
		if !strings.Contains(mgrBody.String(), a) {
			t.Errorf("internal/manager 的生产文件里找不到 %q：`svcmodel.Host` 的这条实现被改名/删掉了？"+
				"（改名后 `app.go` 一起改就能编译过，而端口的那件事没人做）", a)
		}
	}

	// ③ 装配：用例在**控制器**里造（`app` 与 `router` 都不许引用 `service/svc*`）。
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrmodel", "models.go")))
	if !strings.Contains(ctr, "svcmodel.NewDeclared(") {
		t.Error("internal/controller/ctrmodel/models.go 里没有 `svcmodel.NewDeclared(`：写面用例必须在控制器里造出来" +
			"（装配只能自上而下；`app`/`router` 不许引用 `service/svc*`）。")
	}
	for _, m := range []string{"h.declared.CreateModel(", "h.declared.UpdateModel(", "h.declared.DeleteModel("} {
		if !strings.Contains(ctr, m) {
			t.Errorf("internal/controller/ctrmodel/models.go 里找不到 %q：三个写动作必须走用例出口。", m)
		}
	}
	for _, m := range []string{"h.svc.CreateModel(", "h.svc.UpdateModel(", "h.svc.DeleteModel("} {
		if strings.Contains(ctr, m) {
			t.Errorf("internal/controller/ctrmodel/models.go 里仍出现 %q：写面已不在 `ModelSvc` 上——"+
				"这两条路都能编译的那一天，就是「实现收了但出口没换」的那一天。", m)
		}
	}

	ports := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "router.go")))
	// 字段判据必须是**容空白**的正则：gofmt 会把字段对齐成多空格，定长空格的判据会在
	// 格式化之后静默变绿（§3.1.53 与 P3a-2 第一刀各踩过一次）。
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`Store\s+\*store\.Store`),
		regexp.MustCompile(`ModelHost\s+svcmodel\.Host`),
	} {
		if !want.MatchString(ports) {
			t.Errorf("internal/router/router.go 的 `Ports` 里找不到 %s：装配点交出的是**依赖事实**"+
				"（store）与**过渡端口**（Host），用例实例由控制器自己造。", want)
		}
	}
	if regexp.MustCompile(`DeclaredSvc`).MatchString(ports) {
		t.Error("internal/router/router.go 里出现 `DeclaredSvc`：`Ports` 只放依赖事实与窄端口，" +
			"用例接口不外溢到装配点（P4a 那批端口别名已判定为骚操作，别再开这条路）。")
	}

	app := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "app.go")))
	for _, want := range []string{"ModelHost: a.svc", "Store: st"} {
		if !strings.Contains(app, want) {
			t.Errorf("internal/app/app.go 里找不到 %q：模型写面的依赖事实与过渡端口都在装配点交出"+
				"（漏了 Store ⇒ 所有模式都报「清单不可用」；漏了 ModelHost ⇒ 端口没人满足，编译期就该拦）。", want)
		}
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
