package archtest

import (
	"path/filepath"
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
