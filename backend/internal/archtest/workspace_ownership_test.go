package archtest

import (
	"path/filepath"
	"regexp"
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
	// 别名与装配期转发（P4a 正文第二十刀）：`type WorkspaceRootsStore = workspace.WorkspaceRootsStore`
	// 正是第三条硬规矩点名的"端口别名"——一个类型两处定义，接错线时编译器不会拦（两边都满足）。
	// 装配侧同理只有一处入口：`WorkspacePlane().SetRootsStore(...)`。
	settingsMgr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "workspace_settings.go")))
	if strings.Contains(settingsMgr, "type WorkspaceRootsStore =") {
		t.Error("`internal/manager/workspace_settings.go` 里又出现 `type WorkspaceRootsStore =`：" +
			"端口别名为第三条硬规矩所禁（一个类型只有一处定义）。")
	}
	if strings.Contains(settingsMgr, "func (s *Service) SetWorkspaceSettingsStore(") {
		t.Error("`internal/manager` 里又出现 `func (s *Service) SetWorkspaceSettingsStore(`：" +
			"装配期接线也只有一处入口（`WorkspacePlane().SetRootsStore`）。")
	}
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go"))),
		"rootsStore WorkspaceRootsStore") {
		t.Error("`internal/domain/workspace/policy.go` 里找不到 `rootsStore WorkspaceRootsStore`：" +
			"端口的唯一归属是域，判据失锚＝空转。")
	}
	// 策略事实的读取（P4a 正文第二十一刀）：`WorkspaceDir()` / `WorkspaceRoots()` 是**纯事实读取**
	// （无 DTO、无错误码），`Service` 上那两条转发连同零调用点的 `SetWorkspaceRoots` 一起删。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "manager")) {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, dead := range []string{"func (s *Service) WorkspaceDir(", "func (s *Service) WorkspaceRoots(", "func (s *Service) SetWorkspaceRoots("} {
			if strings.Contains(body, dead) {
				t.Errorf("internal/manager/%s 里又出现 `%s`：策略事实只有一处入口"+
					"（`WorkspacePlane()`），多一层转发就会有人读到另一个平面（P4a 正文第二十一刀）。",
					filepath.Base(path), dead)
			}
		}
	}
	// 装配期写事实（P4a 正文第二十二刀）：`SetWorkspacePolicy` 与第十六刀的 `SetBindAddr` 同类——
	// 策略只有一处入口（`WorkspacePlane()`），多一层转发就会出现"策略写进了 A 平面、读的时候看 B 平面"。
	{
		policyMgr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "workspace.go")))
		if strings.Contains(policyMgr, "func (s *Service) SetWorkspacePolicy(") {
			t.Error("`internal/manager/workspace.go` 里又出现 `func (s *Service) SetWorkspacePolicy(`：" +
				"装配期写事实只有一处入口（`WorkspacePlane().SetWorkspacePolicy`）。")
		}
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go"))),
			"func (pl *Plane) SetWorkspacePolicy(dir string, roots []string) error") {
			t.Error("`internal/domain/workspace/policy.go` 里找不到 `SetWorkspacePolicy` 本体：判据失锚＝空转。")
		}
	}

	// 别名与纯函数转发（P4a 正文第二十三刀）：`manager/workspace.go` 里那两个别名
	// （`WorkspaceResolution` / `DefaultWorkspaceRef`）与三个纯函数转发一起删。**别名最危险的
	// 地方不是"多一份定义"，而是"谁是这个类型的主人"变得不可判定**——接错线时两边都编译过。
	wsMgr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "workspace.go")))
	for _, dead := range []string{
		"type WorkspaceResolution =", "type DefaultWorkspaceRef =",
		"func ValidateWorkspace(", "func ResolveWorkspace(", "func DiscardWorkspace(",
	} {
		if strings.Contains(wsMgr, dead) {
			t.Errorf("`internal/manager/workspace.go` 里又出现 `%s`：域是这些类型与纯函数的主人"+
				"（第三条硬规矩禁别名），调用点直接依赖 `internal/domain/workspace`。", dead)
		}
	}
	wsDomain := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go")))
	for _, must := range []string{"type WorkspaceResolution struct", "type DefaultWorkspaceRef struct", "func ValidateWorkspace(", "func ResolveWorkspace(", "func DiscardWorkspace("} {
		if !strings.Contains(wsDomain, must) {
			t.Errorf("`internal/domain/workspace/policy.go` 里找不到 %q：判据失锚＝空转（本体必须在域里）。", must)
		}
	}

	// **别名不许再新开**：`manager` 生产文件里剩下的别名必须**登记在册**（P4a 正文第二十三刀起
	// 用它挡"顺手再加一个别名"，同时如实记下还没清完的那几笔账——清一个删一行）。
	// 判据只认顶格的 `type X = `：方法体里的类型断言/赋值不在此列。
	registeredAliases := map[string]string{
		"CapabilitySelection": "internal/manager/capability_facts.go",
		"SessionRec":          "internal/manager/session_forward.go",
	}
	aliasRe := regexp.MustCompile(`(?m)^type ([A-Za-z0-9_]+) = `)
	aliasSeen := 0
	aliasFound := map[string]bool{}
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "manager")) {
		for _, m := range aliasRe.FindAllStringSubmatch(stripLineComments(readFileOrFatal(t, path)), -1) {
			aliasSeen++
			aliasFound[m[1]] = true
			if _, ok := registeredAliases[m[1]]; !ok {
				t.Errorf("internal/manager/%s 里出现未登记的端口别名 `type %s = `："+
					"第三条硬规矩禁别名（一个类型只有一处定义，接错线时两边都编译过）。"+
					"若是清理过渡期账目，请**先迁走调用点再删这一行**；确需保留请登记进 registeredAliases 并写明理由。",
					filepath.Base(path), m[1])
			}
		}
	}
	// **双向对账**：登记了但磁盘上没有 = 清理时忘删登记行（这种漂移会让"还有几笔账"变成假数，
	// 而下面的"一个都没扫到"只在全清空时才响，中间状态它抓不到）。
	for name := range registeredAliases {
		if !aliasFound[name] {
			t.Errorf("`registeredAliases` 里登记着 %q，但 `internal/manager` 生产文件里已经没有这个别名了："+
				"清完一笔账要**同时删掉登记行**（判据与被判对象同生共死）。", name)
		}
	}
	if aliasSeen == 0 {
		t.Error("`internal/manager` 下一个别名都没扫到：判据失锚＝空转（登记表与扫描面必须同步收敛）。")
	}

	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go"))),
		"func (pl *Plane) WorkspaceRoots() []string") {
		t.Error("`internal/domain/workspace/policy.go` 里找不到 `func (pl *Plane) WorkspaceRoots()`：" +
			"判据失锚＝空转。")
	}

	appGo := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "app.go")))
	if !strings.Contains(appGo, "a.svc.WorkspacePlane().SetRootsStore(") {
		t.Error("`internal/app/app.go` 里找不到 `a.svc.WorkspacePlane().SetRootsStore(`：" +
			"落盘端口必须接到**装配好的那个平面**上（接错一个平面时两边都编译过，表现是设置改了不落盘）。")
	}

	// 写面的三条（P4a 正文第二十七刀）：Rename/Delete/Move 的投影（DTO + 三个错误码）都在域平面，
	// 用例层接口里**不许再留声明**——留着就只是给"一行转发"留位置。
	{
		svcWs := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svcworkspace", "workspace.go")))
		for _, dead := range []string{"RenameRegisteredWorkspace(", "DeleteRegisteredWorkspace(", "MoveRegisteredWorkspace("} {
			if strings.Contains(svcWs, dead) {
				t.Errorf("`svcworkspace` 的接口里又出现 `%s`：这三条写面的投影在域平面里，"+
					"控制器直接 `h.plane.*`（接口里留声明 = 给一行转发留位置）。", dead)
			}
		}
		regMgr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "workspace_registry.go")))
		for _, dead := range []string{"func (s *Service) RenameRegisteredWorkspace(", "func (s *Service) DeleteRegisteredWorkspace(", "func (s *Service) MoveRegisteredWorkspace("} {
			if strings.Contains(regMgr, dead) {
				t.Errorf("`internal/manager/workspace_registry.go` 里又出现 `%s`：那一行转发已删。", dead)
			}
		}
		// 登记表最后一条写面（P4a 正文第二十九刀）：`CreateRegisteredWorkspace` 也走域平面，
		// 接口与 manager 转发一起删（接口里留声明 = `*manager.Service` 还得实现它 = 一行转发）。
		if strings.Contains(svcWs, "CreateRegisteredWorkspace(") {
			t.Error("`svcworkspace` 的接口里又出现 `CreateRegisteredWorkspace(`：投影在域平面里，" +
				"控制器直接 `h.plane.*`。")
		}
		if strings.Contains(regMgr, "func (s *Service) CreateRegisteredWorkspace(") {
			t.Error("`internal/manager/workspace_registry.go` 里又出现 `func (s *Service) CreateRegisteredWorkspace(`：" +
				"那一行转发已删。")
		}

		// 默认工作空间的两条（P4a 正文第二十八刀）：`EnsureDefaultWorkspace` / `DefaultWorkspace`
		// 也走域平面——它们是"这个平面自己的默认项"，读/建都只有一处入口。
		for _, dead := range []string{"func (s *Service) EnsureDefaultWorkspace(", "func (s *Service) DefaultWorkspace("} {
			if strings.Contains(regMgr, dead) {
				t.Errorf("`internal/manager/workspace_registry.go` 里又出现 `%s`：默认工作空间归域平面"+
					"（`WorkspacePlane()`），转发会让「谁建的那一行」不可判定。", dead)
			}
		}
		reg := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "registry.go")))
		for _, must := range []string{"func (pl *Plane) EnsureDefaultWorkspace(", "func (pl *Plane) DefaultWorkspace("} {
			if !strings.Contains(reg, must) {
				t.Errorf("`internal/domain/workspace/registry.go` 里找不到 %q：判据失锚＝空转。", must)
			}
		}

		if !strings.Contains(reg, "func (pl *Plane) CreateRegisteredWorkspace(") {
			t.Error("`internal/domain/workspace/registry.go` 里找不到 `CreateRegisteredWorkspace` 本体：判据失锚＝空转。")
		}
		for _, must := range []string{"func (pl *Plane) RenameRegisteredWorkspace(", "func (pl *Plane) DeleteRegisteredWorkspace(", "func (pl *Plane) MoveRegisteredWorkspace("} {
			if !strings.Contains(reg, must) {
				t.Errorf("`internal/domain/workspace/registry.go` 里找不到 %q：判据失锚＝空转。", must)
			}
		}
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
