package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P2b「工作空间归属」守卫 ———
//
// P2b 把工作空间拆成三刀搬进 `internal/domain/workspace`（策略/校验 → 设置面 → 只读辅助面），
// 中心思想只有一条：**这些行为的实现只有一份，且在域里**。三种失效都不会让既有测试变红：
//
//	① **实现回流**：谁"顺手"在 manager 里再写一遍读目录/校验/落盘——两份实现立刻分叉，
//	   而 `ValidateWorkspace` 那条"浏览得到、创建被拒"的裂缝正是当年踩出来的；
//	② **装配漏接**：`attachStore` 漏了 `planner.SetStore(st)`。表现**只在 sqlite 模式**现形：
//	   `RecentWorkspaces` 把默认工作空间与已登记项当成"未登记"推回来（memory 模式的登记表在域
//	   自己的字段里，所以开发态多半看不见）——2026-10-16 实测就是这么红了一条
//	   `TestRecentWorkspacesExcludesRegistered`；
//	③ **端口没接**：`SetSessionSource` 漏了 ⇒ `RecentWorkspaces` 恒空（"我明明用过这个目录"）。
//
// 判据三段，都 fail-closed（锚找不到即失败，避免改名后守卫静默空转）。

// workspaceDomainAnchors 域侧必须有的入口符号（它们就是"实现搬过去了"的证据）。
var workspaceDomainAnchors = map[string][]string{
	"policy.go": {
		"func ValidateWorkspace(",
		"func ResolveWorkspace(",
		"func (pl *Plane) SetWorkspacePolicy(",
		"func (pl *Plane) PolicySnapshot(",
	},
	"settings.go": {
		"func (pl *Plane) Settings(",
		"func (pl *Plane) UpdateSettings(",
		"func NormalizeSettingsRoots(",
	},
	"view.go": {
		"func (pl *Plane) BrowseWorkspace(",
		"func (pl *Plane) CreateWorkspaceDir(",
		"func (pl *Plane) ListWorkspaceFiles(",
		"func (pl *Plane) RecentWorkspaces(",
	},
	"ports.go": {
		"type SessionSource interface {",
		"ClearWorkspaceAssignment(",
	},
	"registry.go": {
		"func (pl *Plane) WorkspaceRegistry(",
		"func (pl *Plane) CreateRegisteredWorkspace(",
		"func (pl *Plane) DeleteRegisteredWorkspace(",
		"func (pl *Plane) EnsureDefaultWorkspace(",
		"func (pl *Plane) WorkspaceDisplayOf(",
		"func (pl *Plane) ResolveTargetWorkspace(",
	},
}

// workspaceManagerThinFiles manager 侧**只许是薄转发**的文件：它们不得再出现实现痕迹。
var workspaceManagerThinFiles = []string{
	"workspace_view.go", "workspace_settings.go", "workspace_registry.go", "workspace.go",
}

// workspaceManagerImplMarkers 实现痕迹（这些动作现在只许在域里做）。
var workspaceManagerImplMarkers = []string{
	"os.ReadDir(",
	"os.Stat(",
	"os.Mkdir(",
	"filepath.EvalSymlinks(",
	"SaveWorkspaceRoots(",
	"dirStatusOf(",
	"BootstrapDefaultWorkspace(",
}

// TestWorkspaceImplementationLivesInDomain 域侧必须真的有那些入口，且 manager 的薄转发文件里
// 不得出现实现痕迹。
func TestWorkspaceImplementationLivesInDomain(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "domain", "workspace")
	if _, err := filepath.Glob(filepath.Join(domainDir, "*.go")); err != nil {
		t.Fatalf("域目录不存在：%v", err)
	}
	domainFiles := 0
	for file, anchors := range workspaceDomainAnchors {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, file)))
		domainFiles++
		for _, a := range anchors {
			if !strings.Contains(src, a) {
				t.Errorf("internal/domain/workspace/%s 里找不到 %q：工作空间的实现被改名/搬走了？"+
					"（这条守卫的判据就挂在它身上，失锚等于空转）", file, a)
			}
		}
	}
	if domainFiles == 0 {
		t.Fatal("工作空间域一个入口文件都没解析到（判据失效）")
	}

	// manager 侧：薄转发文件里不得再出现实现痕迹。
	managerDir := filepath.Join(root, "internal", "manager")
	for _, f := range workspaceManagerThinFiles {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(managerDir, f)))
		for _, m := range workspaceManagerImplMarkers {
			if strings.Contains(src, m) {
				t.Errorf("internal/manager/%s 里出现实现痕迹 %q：工作空间的浏览/校验/落盘**只有一份实现**，"+
					"在 `internal/domain/workspace`（P2b）。\n"+
					"在 manager 里再写一遍的后果是两份语义静默分叉——当年那条「浏览得到、创建被拒」的裂缝"+
					"就是这么来的。", f, m)
			}
		}
	}
	// ——— 读模型不进用例层：登记表列表（P4a 正文第十八刀）———
	//
	// `WorkspaceRegistry` 曾在 `svcworkspace` 的接口里、由 manager 一行转发到 `workspace.Plane`。
	// 域里那份已经把登记表折成 `dtoworkspace.WorkspaceListResp`（含 default_id / writable），
	// 用例层再包一层就只是一行转发——与第十一刀会话读模型、第十三刀技能目录同一条口径。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "service", "svcworkspace")) {
		body := stripLineComments(readFileOrFatal(t, path))
		if strings.Contains(body, "WorkspaceRegistry()") {
			t.Errorf("`svcworkspace` 里又声明了 `WorkspaceRegistry()`：读模型的投影在域平面里，"+
				"这一层再包一次就是一行转发（%s）。控制器直接经 `h.plane` 取。", filepath.Base(path))
		}
	}
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "manager")) {
		body := stripLineComments(readFileOrFatal(t, path))
		if strings.Contains(body, "func (s *Service) WorkspaceRegistry(") {
			t.Errorf("internal/manager/%s 里又出现 `func (s *Service) WorkspaceRegistry(`："+
				"那一行转发已删（P4a 正文第十八刀）。", filepath.Base(path))
		}
	}
	// 判据不许失锚：域里那份本体必须在。
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "registry.go"))),
		"func (pl *Plane) WorkspaceRegistry() (*dtoworkspace.WorkspaceListResp, error)") {
		t.Error("`internal/domain/workspace/registry.go` 里找不到登记表读模型本体：判据失锚＝空转。")
	}
	// 设置面快照（P4a 正文第十九刀）：域里 `Plane.Settings()` 同样已经折好 DTO（含
	// configured / prefer_roots / source / locked_by），用例层再包一层就只是一行转发。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "service", "svcworkspace")) {
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "WorkspaceSettings()") {
			t.Errorf("`svcworkspace` 里又声明了 `WorkspaceSettings()`：投影在域平面里（%s），"+
				"这一层再包一次就是一行转发。控制器直接经 `h.plane.Settings()` 取。", filepath.Base(path))
		}
	}
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "manager")) {
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "func (s *Service) WorkspaceSettings(") {
			t.Errorf("internal/manager/%s 里又出现 `func (s *Service) WorkspaceSettings(`："+
				"那一行转发已删（P4a 正文第十九刀）。", filepath.Base(path))
		}
	}
	settingsFam := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "settings.go")))
	if !strings.Contains(settingsFam, "func (pl *Plane) Settings() *dtoworkspace.WorkspaceSettingsResp {") {
		t.Error("`internal/domain/workspace/settings.go` 里找不到 `func (pl *Plane) Settings()`：" +
			"判据失锚＝空转。")
	}
	ctrWork := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrworkspace", "workspace.go")))
	if !strings.Contains(ctrWork, "h.plane.Settings()") {
		t.Error("`ctrworkspace` 里找不到 `h.plane.Settings()`：设置面快照要么走域平面、要么走一层转发。")
	}

	if !strings.Contains(ctrWork, "h.plane.WorkspaceRegistry()") {
		t.Error("`ctrworkspace` 里找不到 `h.plane.WorkspaceRegistry()`：登记表要么走域平面、" +
			"要么走一层转发，没有第三条路。")
	}

}

// TestWorkspaceWiringIsAttached 装配链必须真的接上（库句柄 + 会话事实来源）。
//
// 漏登记的后果**只在某一种模式下现形**（sqlite / 有会话时），所以它特别容易活过一批搬迁：
// `RecentWorkspaces` 会安静地把默认工作空间与已登记项当成"未登记"推回来，或者干脆恒空。
func TestWorkspaceWiringIsAttached(t *testing.T) {
	root := backendRoot(t)
	persist := readFileOrFatal(t, filepath.Join(root, "internal", "manager", "persist.go"))
	if !strings.Contains(persist, "s.planner.SetStore(st)") {
		t.Error("`internal/manager/persist.go` 的 `attachStore` 里找不到 `s.planner.SetStore(st)`：" +
			"工作空间域没拿到库句柄。\n" +
			"表现**只在 sqlite 模式**现形——`RecentWorkspaces` 会把默认工作空间与已登记项当成" +
			"「未登记」推回来（memory 模式的登记表在域自己的字段里，开发态多半看不见）。")
	}
	service := readFileOrFatal(t, filepath.Join(root, "internal", "manager", "service.go"))
	if !strings.Contains(service, "SetSessionSource(s)") {
		t.Error("`internal/manager/service.go` 的 `NewService` 里找不到 `SetSessionSource(s)`：" +
			"工作空间域没有会话事实来源，`RecentWorkspaces` 恒空（用户会说「我明明用过这个目录」）。")
	}
}
