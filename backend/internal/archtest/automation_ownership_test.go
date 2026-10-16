package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P2b「自动化归属」守卫 ———
//
// 自动化的定义装载/读写（两种载体）、排期与到期判定、触发一次的认领与写回、运行史与实时事件、
// 调度循环的启停，全部落 `internal/domain/automation`（P2b 第三刀）。manager 侧只剩转发与
// **宿主适配器**（`automation.Host` 的四件事：建会话 / 取消某一轮 / 在跑吗 / 推一条实时事件）。
//
// 三种失效都不会让既有测试变红：
//
//	① **实现回流**：谁在 manager 里"顺手"再写一遍读目录/读写定义——两份实现立刻分叉；
//	② **装配漏接**：`attachStore` 漏了 `automationPlane.SetStore(st)`。表现**只在 sqlite 模式**
//	   现形（定义行读不到 ⇒ 面板空、触发不跑；memory 模式的注册表在域自己的字段里，开发态看不见）；
//	③ **端口没接**：`NewService` 漏了 `SetHost(...)` ⇒ 触发时找不到宿主。这条在**任何一种模式**下
//	   都不报错，只是自动化永远不建会话（"我配了但它从来没跑过"）。
//
// 判据三段，都 fail-closed（锚找不到即失败，避免改名后守卫静默空转）。

// automationDomainAnchors 域侧必须有的入口符号（它们就是"实现搬过去了"的证据）。
var automationDomainAnchors = map[string][]string{
	"automation.go": {
		"func (pl *Plane) LoadAutomationDefinitions(",
		"func (pl *Plane) LoadAutomationDefinitionsFromStore(",
		"func (pl *Plane) WriteAutomation(",
		"func (pl *Plane) DeleteAutomationByID(",
		"func (pl *Plane) DueSnapshot(",
	},
	"sched.go": {
		"func (pl *Plane) StartAutomationScheduler(",
		"func (pl *Plane) StopAutomationScheduler(",
		"func (pl *Plane) Tick(",
		"func (pl *Plane) RunOnce(",
		"func (pl *Plane) FinishAutomationRun(",
	},
	"carrier.go": {
		"func (pl *Plane) SaveOverlay(",
		"func (pl *Plane) claimAutomationOccurrence(",
	},
	"host.go": {
		"type Host interface {",
		"CreateSession(ctx context.Context, req HostSessionReq) (sessionID string, err error)",
	},
	"plane.go": {
		"func (pl *Plane) Mutate(",
		"func (pl *Plane) SetHost(",
	},
}

// automationManagerThinFiles manager 侧**只许是薄转发/适配**的文件。
var automationManagerThinFiles = []string{"automation_forward.go", "automation_host.go"}

// automationManagerImplMarkers 实现痕迹（这些动作现在只许在域里做）。
var automationManagerImplMarkers = []string{
	"os.ReadDir(",
	"os.ReadFile(",
	"os.WriteFile(",
	"os.Remove(",
}

// TestAutomationImplementationLivesInDomain 域侧必须真的有那些入口，且 manager 的薄转发/
// 适配文件里不得出现实现痕迹。
func TestAutomationImplementationLivesInDomain(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "domain", "automation")
	resolved := 0
	for file, anchors := range automationDomainAnchors {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, file)))
		resolved++
		for _, a := range anchors {
			if !strings.Contains(src, a) {
				t.Errorf("internal/domain/automation/%s 里找不到 %q：自动化域的入口被改名/搬走了？"+
					"（这条守卫的判据就挂在它身上，失锚等于空转）", file, a)
			}
		}
	}
	if resolved == 0 {
		t.Fatal("自动化域一个入口文件都没解析到（判据失效）")
	}

	managerDir := filepath.Join(root, "internal", "manager")
	for _, f := range automationManagerThinFiles {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(managerDir, f)))
		for _, m := range automationManagerImplMarkers {
			if strings.Contains(src, m) {
				t.Errorf("internal/manager/%s 里出现实现痕迹 %q：自动化的定义读写**只有一份实现**，"+
					"在 `internal/domain/automation`（P2b）。\n"+
					"在 manager 里再写一遍的后果是两份语义静默分叉（一份写了运行态 overlay、另一份没写）——"+
					"而两边都编译得过。", f, m)
			}
		}
	}
}

// TestAutomationWiringIsAttached 装配链必须真的接上（库句柄 + 宿主端口）。
//
// 两条的失效方式不同、但都静默：
//   - 漏 `SetStore`：**只在 sqlite 模式**现形（定义行读不到）；
//   - 漏 `SetHost`：**任何模式**下都不报错，只是自动化永远不建会话。
func TestAutomationWiringIsAttached(t *testing.T) {
	root := backendRoot(t)
	persist := readFileOrFatal(t, filepath.Join(root, "internal", "manager", "persist.go"))
	if !strings.Contains(persist, "s.automationPlane.SetStore(st)") {
		t.Error("`internal/manager/persist.go` 的 `attachStore` 里找不到 `s.automationPlane.SetStore(st)`：" +
			"自动化域没拿到库句柄。\n" +
			"表现**只在 sqlite 模式**现形——定义行读不到，面板空、触发不跑（memory 模式的注册表在域自己的字段里）。")
	}
	service := readFileOrFatal(t, filepath.Join(root, "internal", "manager", "service.go"))
	if !strings.Contains(service, "SetHost(") {
		t.Error("`internal/manager/service.go` 的 `NewService` 里找不到 `SetHost(...)`：" +
			"自动化域没有宿主端口。\n" +
			"表现是**任何模式**下都不报错，只是自动化永远不建会话（「我配了但它从来没跑过」）。")
	}
}

// ——— P4a 正文第九刀：automation 的**对外面**在 `svcautomation`，不在域里 ———
//
// `domain/automation/plane.go` 的文件头早就写着本域"不负责对外 DTO 与错误码出口
// （`service/svc*` 与 `dto/*`）"——但 `rest.go` 当初整文件跟着 P2b 搬进了域里，
// 于是那条口径在代码上不成立。这一刀把那一面（请求形态校验 / 错误码出口 / DTO 折叠 / 排序）
// 上提到 `internal/service/svcautomation`，域只留领域动作。
//
// 三种失效都不会让既有测试变红：
//
//	① **回流**：谁在域里又写一遍 `automation_not_found` 或 `InvalidParamErr(`——两份出口立刻分叉
//	   （例如一边 40401、另一边 40001），而"哪边生效"取决于调用路径，两边各自都有测试；
//	② **装配回潮**：`Ports` 那一格写回 svc 类型（`svcautomation.AutomationSvc`）——装配点为了声明
//	   一格字段而 import 用例包，`router`/`app` 不得引用 `service/svc*` 这条口径当场破掉；
//	③ **用例没人造**：控制器不再调 `svcautomation.New(`（拿到的 `plane` 直接当用例用，
//	   或者干脆绕开用例层）——于是 `AutomationSvc` 的实现只剩测试里的替身。
//
// 判据四段，都 fail-closed（锚找不到即失败：改名/搬迁会让守卫静默空转）。

// automationUseCaseAnchors 用例层必须有的实现锚（**私有实现**，不是薄转发）。
var automationUseCaseAnchors = []string{
	"func New(plane *domauto.Plane) AutomationSvc",
	"var _ AutomationSvc = (*Service)(nil)",
	"func (s *Service) ListAutomations() ([]dtoautomation.AutomationResp, error)",
	`definition.RawString(req, "id")`,
	"definition.RawPatch(req)",
	"func (s *Service) PreviewAutomation(",
	"s.plane.RunNow(ctx, id)",
	`"automation_not_found"`,
	`s.plane.Recs()`,
}

// automationApiFaceMarkers 对外面的痕迹（**不许**再出现在域里）。
//
// 只列这一刀真的搬走的那三样：请求形态校验（`InvalidParamErr`）、原文 patch 的折叠
// （`RawPatch`）、写面 DTO 的签名（`AutomationWriteReq`）。
//
// **刻意不列 `"automation_not_found"`**：域里的**运行面**（`RunNow` / 调度 tick）自己也要拒绝
// 未知 id / 坏定义 / 已停用，那三条码是领域的判决，用例层只是**透传**——把它列成"不许出现"
// 会逼着域去 import 用例层（反向依赖）。真正要守的是"两侧同码"，见下面的 parity 判据。
var automationApiFaceMarkers = []string{
	"definition.InvalidParamErr(",
	"definition.RawPatch(req)",
	"dtoautomation.AutomationWriteReq",
}

// automationRunFaceCodes 运行面拒绝的码：域里判、用例层透传，**两侧必须逐字同码**。
//
// 分叉的表现是同一个原因在两条路径上给出不同的码（`POST /automations/:id/run` 报 40401、
// 而列表里那条"未知 id"报别的），客户端只能按码分支，于是其中一条路径的行为无法预期。
// 只列**两侧都在铸**的两个码：读面/写面（用例层铸）与运行面（域铸）都会用到它们。
// `automation_disabled` 刻意不列——只有运行面会拒绝"已停用"，用例层只是把那个 error
// 原样透传（不透传就会丢码），所以它在用例层没有字面量，列进来只会假红。
var automationRunFaceCodes = []string{`"automation_not_found"`, `"definition_invalid"`}

// TestAutomationApiFaceLivesInAutomationSvc 见上方注释。
func TestAutomationApiFaceLivesInAutomationSvc(t *testing.T) {
	root := backendRoot(t)
	svcDir := filepath.Join(root, "internal", "service", "svcautomation")
	svcBody := dirProductionBody(t, svcDir)
	for _, a := range automationUseCaseAnchors {
		if !strings.Contains(svcBody, a) {
			t.Errorf("`internal/service/svcautomation` 里找不到 %q：应用例面被改名/搬走了"+
				"（这条守卫的判据挂在它身上，失锚等于空转）。", a)
		}
	}
	if !strings.Contains(svcBody, `"github.com/morehao/ark-mate/internal/domain/automation"`) &&
		!strings.Contains(svcBody, `domauto "github.com/morehao/ark-mate/internal/domain/automation"`) {
		t.Error("`internal/service/svcautomation` 没有 import `internal/domain/automation`：" +
			"用例层必须调用**领域动作**（WriteAutomation / AutomationByID / Recs / RunNow），" +
			"而不是自己再实现一遍装载与排期。")
	}

	// ① 域里不得再有对外面的痕迹，同时域自己的锚仍要在（否则"扫描面为空"也会绿）。
	domDir := filepath.Join(root, "internal", "domain", "automation")
	domBody := dirProductionBody(t, domDir)
	for _, m := range automationApiFaceMarkers {
		if strings.Contains(domBody, m) {
			t.Errorf("`internal/domain/automation` 里出现对外面的痕迹 %q：对外 DTO 与错误码出口归 "+
				"`service/svcautomation`（域的文件头写着这条口径）。两边各写一份的表现是"+
				"错误码或折叠口径静默分叉。", m)
		}
	}
	// parity：运行面的码域里判、用例层透传——两份文案可以并存（域不能 import 用例层），
	// 但**码必须逐字相同**。
	for _, c := range automationRunFaceCodes {
		if !strings.Contains(domBody, c) || !strings.Contains(svcBody, c) {
			t.Errorf("运行面拒绝码 %s 没有在域与用例层**两侧**同时出现（域里有=%v，用例层有=%v）："+
				"域判它、用例层透传，缺哪一侧都说明这条判决被改写或漏接了。",
				c, strings.Contains(domBody, c), strings.Contains(svcBody, c))
		}
	}
	// 域自己的领域动作锚仍要在（否则"扫描面为空"也会绿）。
	for _, a := range []string{"func (pl *Plane) WriteAutomation(", "func (pl *Plane) Recs() ", "func (pl *Plane) RunNow("} {
		if !strings.Contains(domBody, a) {
			t.Errorf("`internal/domain/automation` 里找不到领域动作 %q：判据失锚（域被搬走/改名了？）", a)
		}
	}
	if _, err := os.Stat(filepath.Join(domDir, "rest.go")); err == nil {
		t.Error("`internal/domain/automation/rest.go` 又存在了：对外面的实现只有一份，" +
			"在 `internal/service/svcautomation`（P4a 正文第九刀）。")
	}

	// ② 装配点只放依赖事实。
	portsSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "router.go")))
	if !regexp.MustCompile(`AutomationPlane\s+\*automation\.Plane`).MatchString(portsSrc) {
		t.Error("`internal/router/router.go` 的 `Ports` 里找不到 `AutomationPlane *automation.Plane`：" +
			"自动化那一格必须是**依赖事实**，用例在控制器里造。")
	}
	if strings.Contains(portsSrc, "svcautomation") {
		t.Error("`internal/router/router.go` 里出现 `svcautomation`：装配点不认识用例层（P4a 正文第九刀）。")
	}
	routesSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "routes_automation.go")))
	if !strings.Contains(routesSrc, "ctrautomation.NewAutomationCtr(ports.AutomationPlane)") {
		t.Error("`internal/router/routes_automation.go` 必须把 `ports.AutomationPlane` 交给控制器：" +
			"交别的（或漏交）会让平面为 nil，第一次取数就是 nil panic。")
	}

	// ③ 用例实例必须在**控制器的构造函数里**造（判据钉在函数体上，不是整文件：
	//    同包一个中间函数就能把整文件的字面量判据喂绿——svcsystem 那一刀实测过）。
	ctrSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrautomation", "automation.go")))
	body := funcBodyOf(t, ctrSrc, "func NewAutomationCtr(")
	if !strings.Contains(body, "svcautomation.New(") {
		t.Errorf("`ctrautomation.NewAutomationCtr` 的函数体里找不到 `svcautomation.New(`："+
			"用例实例必须在控制器里造（交给 `automationCtr` 的必须是**用例**，不是域平面）。实际体：\n%s", body)
	}
}

// dirProductionBody 拼出一个目录下**全部生产 .go**（剥注释）的正文——判据要跨文件找锚时用它。
//
// fail-closed：一个生产文件都没有即 Fatal（目录搬走/改名的最坏表现是"守卫照样绿、
// 只是不再检查任何东西"）。
func dirProductionBody(t *testing.T, dir string) string {
	t.Helper()
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", dir)
	}
	var b strings.Builder
	for _, path := range files {
		b.WriteString(stripLineComments(readFileOrFatal(t, path)))
	}
	return b.String()
}
