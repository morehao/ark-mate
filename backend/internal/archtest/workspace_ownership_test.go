package archtest

import (
	"os"
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
		"func ValidateWorkspaceName(",
		"func ResolveWorkspace(",
		"func (pl *Plane) SetWorkspacePolicy(",
		"func (pl *Plane) PolicySnapshot(",
		"func (pl *Plane) CreateRoot(",
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
		"func (pl *Plane) CreateWorkspace(",
		"func (pl *Plane) CreateNamedWorkspace(",
		"func (pl *Plane) CreateRegisteredWorkspace(",
		"func (pl *Plane) DeleteRegisteredWorkspace(",
		"func (pl *Plane) EnsureDefaultWorkspace(",
		"func (pl *Plane) WorkspaceDisplayOf(",
		"func (pl *Plane) ResolveTargetWorkspace(",
	},
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

	// manager 侧：整个生产目录都不得再出现实现痕迹（扫描面按目录，不按写死的文件名——
	// 转发文件已删，按文件名读会直接 Fatal，而"换个文件放回来"反而溜过去）。
	managerDir := filepath.Join(root, "internal", "app", "run")
	mgrAll := dirProductionBody(t, managerDir)
	for _, m := range workspaceManagerImplMarkers {
		if strings.Contains(mgrAll, m) {
			t.Errorf("`internal/app/run` 里出现实现痕迹 %q：工作空间的浏览/校验/落盘**只有一份实现**，"+
				"在 `internal/domain/workspace`（P2b）。\n"+
				"在 manager 里再写一遍的后果是两份语义静默分叉——当年那条「浏览得到、创建被拒」的裂缝"+
				"就是这么来的。", m)
		}
	}
	// ——— 读模型不进用例层：登记表列表（P4a 正文第十八刀）———
	//
	// `WorkspaceRegistry` 曾在 `svcworkspace` 的接口里、由 manager 一行转发到 `workspace.Plane`；
	// 那个包已在第三十四刀整体删除（见本文件末尾的"端口包不存在"判据）。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "app", "run")) {
		body := stripLineComments(readFileOrFatal(t, path))
		if strings.Contains(body, "func (s *Service) WorkspaceRegistry(") {
			t.Errorf("internal/app/run/%s 里又出现 `func (s *Service) WorkspaceRegistry(`："+
				"那一行转发已删（P4a 正文第十八刀）。", filepath.Base(path))
		}
	}
	// 判据不许失锚：域里那份本体必须在。
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "registry.go"))),
		"func (pl *Plane) WorkspaceRegistry() (*WorkspaceList, error)") {
		t.Error("`internal/domain/workspace/registry.go` 里找不到登记表读模型本体：判据失锚＝空转。")
	}
	// 只读辅助四条（P4a 正文第三十二刀）：浏览 / 建目录 / 列内容 / 最近使用——接口声明与 manager
	// 转发一起删，控制器直接 `h.plane.*`。它们虽然**带错误出口**，但码在域里铸（`workspace_invalid`
	// / `workspace_exists` / `workspace_not_found`）；v3.2 刀 6 起域返回的是**无 tag 视图**，
	// 对外 DTO 由 `svcworkspace` 投影——判据因此钉"域侧本体在"，不再钉 DTO 类型名。
	if vm := mgrAll; strings.Contains(vm, "func (s *Service) BrowseWorkspace(") ||
		strings.Contains(vm, "func (s *Service) CreateWorkspaceDir(") || strings.Contains(vm, "func (s *Service) ListWorkspaceFiles(") ||
		strings.Contains(vm, "func (s *Service) RecentWorkspaces(") {
		t.Error("`internal/app/run` 里又出现只读辅助面的转发：那四条已删（扫描面是整目录）。")
	}
	view := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "view.go")))
	for _, must := range []string{"func (pl *Plane) BrowseWorkspace(", "func (pl *Plane) CreateWorkspaceDir(", "func (pl *Plane) ListWorkspaceFiles(", "func (pl *Plane) RecentWorkspaces("} {
		if !strings.Contains(view, must) {
			t.Errorf("`internal/domain/workspace/view.go` 里找不到 %q：判据失锚＝空转。", must)
		}
	}

	// 设置面写（P4a 正文第三十一刀）：`UpdateWorkspaceSettings` 同样归域平面（写面：校验 + 三个
	// 语义码 + 落盘都在域里），接口声明与 manager 转发一起删。控制器侧不再有替身可伪造语义码 ——
	// "锁定 / 只读 / 非法目录"三条由配置面测试用真策略钉。
	if strings.Contains(mgrAll, "func (s *Service) UpdateWorkspaceSettings(") {
		t.Error("`internal/app/run` 里又出现 `UpdateWorkspaceSettings(` 转发：那一行已删（扫描面是整目录）。")
	}
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "settings.go"))),
		"func (pl *Plane) UpdateSettings(roots []string) (*SettingsView, error)") {
		t.Error("`internal/domain/workspace/settings.go` 里找不到 `func (pl *Plane) UpdateSettings(...)`：判据失锚＝空转。")
	}

	// 设置面快照（P4a 正文第十九刀）：域里 `Plane.Settings()` 折出**设置视图**（含
	// configured / prefer_roots / source / locked_by）；v3.2 刀 6 起投影归 `svcworkspace`。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "app", "run")) {
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "func (s *Service) WorkspaceSettings(") {
			t.Errorf("internal/app/run/%s 里又出现 `func (s *Service) WorkspaceSettings(`："+
				"那一行转发已删（P4a 正文第十九刀）。", filepath.Base(path))
		}
	}
	settingsFam := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "settings.go")))
	if !strings.Contains(settingsFam, "func (pl *Plane) Settings() *SettingsView {") {
		t.Error("`internal/domain/workspace/settings.go` 里找不到 `func (pl *Plane) Settings()`：" +
			"判据失锚＝空转。")
	}
	// 别名与装配期转发（P4a 正文第二十刀）：`type WorkspaceRootsStore = workspace.WorkspaceRootsStore`
	// 正是第三条硬规矩点名的"端口别名"——一个类型两处定义，接错线时编译器不会拦（两边都满足）。
	// 装配侧同理只有一处入口：`WorkspacePlane().SetRootsStore(...)`。
	settingsMgr := mgrAll
	if strings.Contains(settingsMgr, "type WorkspaceRootsStore =") {
		t.Error("`internal/app/run` 里又出现 `type WorkspaceRootsStore =`：" +
			"端口别名为第三条硬规矩所禁（一个类型只有一处定义）。")
	}
	if strings.Contains(settingsMgr, "func (s *Service) SetWorkspaceSettingsStore(") {
		t.Error("`internal/app/run` 里又出现 `func (s *Service) SetWorkspaceSettingsStore(`：" +
			"装配期接线也只有一处入口（`WorkspacePlane().SetRootsStore`）。")
	}
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go"))),
		"rootsStore WorkspaceRootsStore") {
		t.Error("`internal/domain/workspace/policy.go` 里找不到 `rootsStore WorkspaceRootsStore`：" +
			"端口的唯一归属是域，判据失锚＝空转。")
	}
	// 策略事实的读取（P4a 正文第二十一刀）：`WorkspaceDir()` / `WorkspaceRoots()` 是**纯事实读取**
	// （无 DTO、无错误码），`Service` 上那两条转发连同零调用点的 `SetWorkspaceRoots` 一起删。
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "app", "run")) {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, dead := range []string{"func (s *Service) WorkspaceDir(", "func (s *Service) WorkspaceRoots(", "func (s *Service) SetWorkspaceRoots("} {
			if strings.Contains(body, dead) {
				t.Errorf("internal/app/run/%s 里又出现 `%s`：策略事实只有一处入口"+
					"（`WorkspacePlane()`），多一层转发就会有人读到另一个平面（P4a 正文第二十一刀）。",
					filepath.Base(path), dead)
			}
		}
	}
	// 装配期写事实（P4a 正文第二十二刀）：`SetWorkspacePolicy` 与第十六刀的 `SetBindAddr` 同类——
	// 策略只有一处入口（`WorkspacePlane()`），多一层转发就会出现"策略写进了 A 平面、读的时候看 B 平面"。
	{
		if strings.Contains(mgrAll, "func (s *Service) SetWorkspacePolicy(") {
			t.Error("`internal/app/run` 里又出现 `func (s *Service) SetWorkspacePolicy(`：" +
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
	wsMgr := mgrAll
	for _, dead := range []string{
		"type WorkspaceResolution =", "type DefaultWorkspaceRef =",
		"func ValidateWorkspace(", "func ResolveWorkspace(", "func DiscardWorkspace(",
	} {
		if strings.Contains(wsMgr, dead) {
			t.Errorf("`internal/app/run/workspace.go` 里又出现 `%s`：域是这些类型与纯函数的主人"+
				"（第三条硬规矩禁别名），调用点直接依赖 `internal/domain/workspace`。", dead)
		}
	}
	wsDomain := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go")))
	for _, must := range []string{"type WorkspaceResolution struct", "type DefaultWorkspaceRef struct", "func ValidateWorkspace(", "func ResolveWorkspace(", "func DiscardWorkspace("} {
		if !strings.Contains(wsDomain, must) {
			t.Errorf("`internal/domain/workspace/policy.go` 里找不到 %q：判据失锚＝空转（本体必须在域里）。", must)
		}
	}

	// **别名归零**（P4b）：过渡期的两个端口别名（`CapabilitySelection` / `SessionRec`）已
	// 全部删掉，调用点直接依赖 `domain/session`。判据只认顶格的 `type X = `：方法体里的
	// 类型断言/赋值不在此列。再出现任何别名即红——"一个类型只有一处定义"是硬规矩。
	aliasRe := regexp.MustCompile(`(?m)^type ([A-Za-z0-9_]+) = `)
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "app", "run")) {
		for _, m := range aliasRe.FindAllStringSubmatch(stripLineComments(readFileOrFatal(t, path)), -1) {
			t.Errorf("internal/app/run/%s 里出现端口别名 `type %s = `："+
				"第三条硬规矩禁别名（一个类型只有一处定义，接错线时两边都编译过）。"+
				"调用点应直接依赖 `internal/domain/session`。", filepath.Base(path), m[1])
		}
	}

	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "policy.go"))),
		"func (pl *Plane) WorkspaceRoots() []string") {
		t.Error("`internal/domain/workspace/policy.go` 里找不到 `func (pl *Plane) WorkspaceRoots()`：" +
			"判据失锚＝空转。")
	}

	// ——— v3.1：工作空间面有**用例包**，控制器不 import 领域 ———
	//
	// P4a 曾把 `svcworkspace` 删除（实现全在 `domain/workspace`，控制器直接持平面）；v3.1 §3.2.1
	// 规则 1 要求控制器不得 import `internal/domain`，因此这一层以**窄接口**的形式回来：
	// 控制器只认 `workspace.WorkspaceSvc`，平面仍归领域。
	svcDir := filepath.Join(root, "internal", "app", "workspace")
	if _, err := os.Stat(svcDir); err != nil {
		t.Error("`internal/app/workspace` 不存在了：用例层是控制器与领域之间的唯一连接（v3.1 §3.2.1）。")
	}
	ctrWs := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrworkspace", "workspace.go")))
	if strings.Contains(ctrWs, "internal/domain/workspace") {
		t.Error("`ctrworkspace` 又 import 了 `internal/domain/workspace`：控制器只认用例接口。")
	}
	asmWs := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "services.go")))
	if !strings.Contains(asmWs, "workspace.New(") {
		t.Error("`internal/bootstrap/assembly/services.go` 里找不到 `workspace.New(`。")
	}
	asmGo := dirProductionBody(t, filepath.Join(root, "internal", "bootstrap", "assembly"))
	if !strings.Contains(asmGo, "as.planes.Workspaces.SetRootsStore(") {
		t.Error("`internal/bootstrap/assembly` 里找不到 `as.planes.Workspaces.SetRootsStore(`：" +
			"落盘端口必须接到**装配好的那个平面**上（接错一个平面时两边都编译过，表现是设置改了不落盘）。")
	}

	// 写面的三条（P4a 正文第二十七刀）：Rename/Delete/Move 的投影（DTO + 三个错误码）都在域平面，
	// 用例层接口里**不许再留声明**——留着就只是给"一行转发"留位置。
	{
		// 扫描面是**整个生产目录**（workspace_registry.go 已删；痕迹换文件放回来也要红）。
		regMgr := dirProductionBody(t, filepath.Join(root, "internal", "app", "run"))
		for _, dead := range []string{"func (s *Service) RenameRegisteredWorkspace(", "func (s *Service) DeleteRegisteredWorkspace(", "func (s *Service) MoveRegisteredWorkspace("} {
			if strings.Contains(regMgr, dead) {
				t.Errorf("`internal/app/run` 里又出现 `%s`：那一行转发已删。", dead)
			}
		}
		// 登记表最后一条写面（P4a 正文第二十九刀）：`CreateRegisteredWorkspace` 也走域平面，
		// 接口与 manager 转发一起删（接口里留声明 = `*manager.Service` 还得实现它 = 一行转发）。
		if strings.Contains(regMgr, "func (s *Service) CreateRegisteredWorkspace(") {
			t.Error("`internal/app/run/workspace_registry.go` 里又出现 `func (s *Service) CreateRegisteredWorkspace(`：" +
				"那一行转发已删。")
		}

		// 默认工作空间的两条（P4a 正文第二十八刀）：`EnsureDefaultWorkspace` / `DefaultWorkspace`
		// 也走域平面——它们是"这个平面自己的默认项"，读/建都只有一处入口。
		for _, dead := range []string{"func (s *Service) EnsureDefaultWorkspace(", "func (s *Service) DefaultWorkspace("} {
			if strings.Contains(regMgr, dead) {
				t.Errorf("`internal/app/run/workspace_registry.go` 里又出现 `%s`：默认工作空间归域平面"+
					"（`WorkspacePlane()`），转发会让「谁建的那一行」不可判定。", dead)
			}
		}
		reg := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "registry.go")))
		for _, must := range []string{"func (pl *Plane) EnsureDefaultWorkspace(", "func (pl *Plane) DefaultWorkspace("} {
			if !strings.Contains(reg, must) {
				t.Errorf("`internal/domain/workspace/registry.go` 里找不到 %q：判据失锚＝空转。", must)
			}
		}

		// 两条**包内私有**转发（P4a 正文第三十刀）：`resolveTargetWorkspace` / `workspaceIDByPath`
		// 没有接口声明，所以它们不会被"接口里少一行"这类判据抓到——只能按符号名点掉。
		for _, dead := range []string{"func (s *Service) resolveTargetWorkspace(", "func (s *Service) workspaceIDByPath("} {
			if strings.Contains(regMgr, dead) {
				t.Errorf("`internal/app/run/workspace_registry.go` 里又出现 `%s`：包内调用点直接写"+
					"`s.planner.*`／`WorkspacePlane()`，私有转发同样是「多一层就多一处可读错的地方」。", dead)
			}
		}
		for _, must := range []string{"func (pl *Plane) ResolveTargetWorkspace(", "func (pl *Plane) WorkspaceIDByPath("} {
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

	ctrWork := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrworkspace", "workspace.go")))
	if !strings.Contains(ctrWork, "h.svc.Settings()") {
		t.Error("`ctrworkspace` 里找不到 `h.svc.Settings()`：设置面快照必须经用例接口（v3.1 §3.2.1）。")
	}
	if !strings.Contains(ctrWork, "h.svc.Registry()") {
		t.Error("`ctrworkspace` 里找不到 `h.svc.Registry()`：登记表必须经用例接口。")
	}

}

// TestWorkspaceWiringIsAttached 装配链必须真的接上（库句柄 + 会话事实来源）。
//
// 漏登记的后果**只在某一种模式下现形**（sqlite / 有会话时），所以它特别容易活过一批搬迁：
// `RecentWorkspaces` 会安静地把默认工作空间与已登记项当成"未登记"推回来，或者干脆恒空。
func TestWorkspaceWiringIsAttached(t *testing.T) {
	root := backendRoot(t)
	persist := readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "planes", "planes.go"))
	// v3.2 刀 6 起交下去的是**适配体**：十三读写法由内嵌的 `*store.Store` 提升，另加三条
	// `Is*` 把载体哨兵错误翻成领域判决。判据钉"确实交下去、且适配体包着 st"——只钉
	// `SetStore(st)` 会在适配体出现后失锚。C3 4.5 后半适配体搬到
	// `adapters/outbound/workspaces`，装配点只经它的构造口（`workspaces.NewStore(st)`）。
	if !strings.Contains(persist, "p.Workspaces.SetStore(workspaces.NewStore(st))") {
		t.Error("`internal/bootstrap/assembly/planes/planes.go` 的 `EnableStore` 里找不到 `p.Workspaces.SetStore(workspaces.NewStore(st))`：" +
			"工作空间域没拿到库句柄。\n" +
			"表现**只在 sqlite 模式**现形——`RecentWorkspaces` 会把默认工作空间与已登记项当成" +
			"「未登记」推回来（memory 模式的登记表在域自己的字段里，开发态多半看不见）。")
	}
	service := readFileOrFatal(t, filepath.Join(root, "internal", "app", "run", "service.go"))
	if !strings.Contains(service, "SetSessionSource(s)") {
		t.Error("`internal/app/run/service.go` 的 `NewService` 里找不到 `SetSessionSource(s)`：" +
			"工作空间域没有会话事实来源，`RecentWorkspaces` 恒空（用户会说「我明明用过这个目录」）。")
	}
}

// ——— v27 · ADR-2「创建分两模式」与 ADR-3「落盘根」 ———
//
// 两条守卫各钉一种**静默分叉**（都不会让既有测试变红）：
//
//	① **两条建目录路径的问法不一样**：本仓有两处"造一个新目录"（目录浏览面的「新建文件夹」
//	   与工作空间面的「新建工作空间…」）。它们问的是同一个问题（"这个名字能不能当一段目录名"），
//	   而各写一份判据的表现不是报错——是接受集悄悄分叉，于是「文件夹能建、工作空间建不了」
//	   （或反过来）看起来像两个不相干的 bug；
//	② **落盘根有第二个来源**：`create_root` 一旦在 `registry.go` 里再读一次 env/flag，
//	   就会出现「设置里显示一个位置、新建却落到别处」，而两处都"看起来在工作"。

// TestWorkspaceCreateModesAreExplicit 守卫 1（ADR-2）：两模式显式 + 名字校验单点。
func TestWorkspaceCreateModesAreExplicit(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "domain", "workspace")
	read := func(name string) string {
		return stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, name)))
	}

	// ① 唯一入口在域里，两条路各自是一个方法（`CreateWorkspace` 只做分派）。
	registry := read("registry.go")
	for _, anchor := range []string{
		"func (pl *Plane) CreateWorkspace(",
		"func (pl *Plane) CreateNamedWorkspace(",
		"func (pl *Plane) CreateRegisteredWorkspace(",
	} {
		if !strings.Contains(registry, anchor) {
			t.Fatalf("registry.go 里找不到 %q（fail-closed：两模式的唯一入口/两条实现都在这里）", anchor)
		}
	}

	// ② 名字校验**只有一处声明**（全 internal 数）。
	declared := 0
	var declaredAt string
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		if strings.HasSuffix(p, "_test.go") || strings.Contains(filepath.ToSlash(p), "/archtest/") {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), "func ValidateWorkspaceName(") {
			declared++
			declaredAt = p
		}
	}
	if declared != 1 {
		t.Fatalf("`func ValidateWorkspaceName(` 在 internal/ 里声明了 %d 处（want 1），最后一处 %s：\n"+
			"这个名字校验必须单点——两条建目录路径（「新建文件夹」与「新建工作空间…」）问的是同一个问题，"+
			"各写一份的表现是**接受集悄悄分叉**，而不是报错", declared, declaredAt)
	}

	// ③ **两条路都在调它**（这才是"单点"的证据：有一处没调，就说明那一处自己判了）。
	if !strings.Contains(registry, "ValidateWorkspaceName(") {
		t.Fatalf("registry.go 的 `name` 模式没有调用 `ValidateWorkspaceName(`：它必然另写了一份名字校验")
	}
	view := read("view.go")
	if !strings.Contains(view, "ValidateWorkspaceName(") {
		t.Fatalf("view.go 的 `CreateWorkspaceDir`（目录浏览面的「新建文件夹」）没有调用 `ValidateWorkspaceName(`：" +
			"它与「新建工作空间…」问的是同一个问题，各判一份就会分叉")
	}

	// ④ `name` 模式**不得静默复用**已有目录：必须显式看它存不存在、且报 40901。
	if !strings.Contains(registry, "os.Lstat(target)") {
		t.Fatalf("`CreateNamedWorkspace` 里没有对目标路径的显式存在性检查（`os.Lstat(target)`）：\n" +
			"`os.Mkdir` 的 `IsExist` 只兜住「恰好并发」的那一瞬，而「同名目录早就存在」必须在这里被明确拒绝——" +
			"静默复用会让用户以为「我新建的那个」就是「原来那个」")
	}
	if !strings.Contains(registry, "workspaceExistsErr(") {
		t.Fatalf("`CreateNamedWorkspace` 没有走 `workspaceExistsErr(`（40901 / workspace_exists）：" +
			"与本仓「同名目录已存在 → 40901，绝不静默复用」的既有口径必须一致")
	}

	// ⑤ 分派判决不许写回控制器（控制器只做绑定与回响应）。
	//
	// 判据只看 **`Create` 这个 handler 的函数体**：同文件里的 `CreateDir` 处理的是**另一种请求**
	// （`WorkspaceDirCreateReq` 的 `name` 是"在已存在目录下建一层"，不是"新建工作空间"），
	// 按文件扫会把那一处正常的字段读取当成回流（本守卫第一版就是这么假红的）。
	ctrDir := filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrworkspace")
	foundCreate := false
	for _, p := range goFilesUnder(t, ctrDir) {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, p))
		body, ok := goFuncBody(src, "func (h *workspaceCtr) Create(c *gin.Context) {")
		if !ok {
			continue
		}
		foundCreate = true
		if strings.Contains(body, "req.Name") || strings.Contains(body, "req.Path") {
			t.Errorf("%s 的 `Create` handler 里读了 `req.Path`/`req.Name`：`path` / `name` 的二选一是"+
				"**业务判决**，归域里的 `Plane.CreateWorkspace`。放在控制器里会让「两模式」出现在一个"+
				"不懂工作空间语义的地方，而且第二次加模式时必然再写一遍同一段分派", filepath.Base(p))
		}
		if !strings.Contains(body, "h.svc.Create(req)") {
			t.Errorf("%s 的 `Create` handler 里没有 `h.svc.Create(req)`：创建入口必须经用例接口", filepath.Base(p))
		}
	}
	if !foundCreate {
		t.Fatalf("%s 里找不到 `Create` handler（fail-closed：改名/搬迁会让本判据静默空转）", ctrDir)
	}
}

// TestWorkspaceCreateRootIsDeclared 守卫 2（ADR-3）：`create_root` 取值单点 + 前端为空时隐藏入口。
func TestWorkspaceCreateRootIsDeclared(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "domain", "workspace")

	// ① 取值单点：`pl.dir` 只许出现在 policy.go（它是"落盘根"的唯一读者）。
	for _, p := range goFilesUnder(t, domainDir) {
		if strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "policy.go" {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, p))
		if strings.Contains(src, "pl.dir") {
			t.Errorf("%s 里直接读了 `pl.dir`：落盘根的取值只有一处实现（`Plane.CreateRoot`），\n"+
				"别处再读一次就会出现「设置里显示一个位置、新建却落到别处」——而两处都「看起来在工作」", filepath.Base(p))
		}
	}

	// ② 域里不许再读 env/flag（换来源时只改 `createRootLocked`，不改调用方）。
	//
	// **`picker.go` 刻意不在扫描面里**：它采样的是**宿主事实**（回环绑定 / SSH / 显示会话），
	// 其中 SSH 那几个变量只能从 env 读——那与"工作空间落在哪"是两件事，扫进来只会是噪声
	// （同 `desktop_host_path_test.go` 里"构建期脚本刻意不扫"的那条理由）。
	for _, p := range goFilesUnder(t, domainDir) {
		if strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "picker.go" {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, p))
		for _, banned := range []string{"os.Getenv(", "os.LookupEnv(", "flag."} {
			if strings.Contains(src, banned) {
				t.Errorf("%s 里出现了 %q：工作空间的策略来源由装配根交下来（`SetWorkspacePolicy`），"+
					"域里自己读环境变量会让「配置说一套、运行另一套」", filepath.Base(p), banned)
			}
		}
	}

	// ③ 快照必须把 create_root 一并发出来（三样一起取，见 PolicySnapshot 注释），且 Settings 用它。
	policy := stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, "policy.go")))
	if !strings.Contains(policy, "func (pl *Plane) PolicySnapshot() (effective, configured []string, provisionDir, createRoot string)") {
		t.Fatalf("`PolicySnapshot` 的签名里没有 `createRoot` 出参：设置面与列表响应都要它，" +
			"分三次取会让面板看到「配置改了、这一项还没改」的中间态")
	}
	if !strings.Contains(policy, "func (pl *Plane) createRootLocked() string") {
		t.Fatalf("policy.go 里找不到 `createRootLocked`：落盘根的解析必须单点（`CreateRoot` 与 `PolicySnapshot` 共用它）")
	}
	settings := stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, "settings.go")))
	if !strings.Contains(settings, "CreateRoot:        createRoot") && !strings.Contains(settings, "CreateRoot:") {
		t.Fatalf("settings.go 的 `Settings()` 没有填 `CreateRoot`：设置面读不到落盘根")
	}

	// ④ 前端：落盘根为空时**不渲染**「新建工作空间…」入口（Go 读 TS）。
	feRoot := filepath.Join(root, "..", "frontend")
	addPath := filepath.Join(feRoot, "packages", "console", "src", "components", "AddWorkspaceButton.tsx")
	add := stripTSComments(readFileOrFatal(t, addPath))
	if !strings.Contains(add, "create_root") {
		t.Fatalf("%s 没有读 `create_root`：区头那颗按钮无从知道「新建该不该给入口」", addPath)
	}
	if !strings.Contains(add, "createRoot ?") {
		t.Fatalf("%s 里没有 `createRoot ?` 这个条件渲染：\n"+
			"落盘根为空时「新建工作空间…」是一个**必然失败**的按钮（ADR-3 的入口纪律，同 ADR-11）。"+
			"把入口改成常显不会让任何测试变红，只会让用户点了才发现不行", addPath)
	}
	if !strings.Contains(add, "新建工作空间…") {
		t.Fatalf("%s 里找不到「新建工作空间…」这一项（被判据引用的目标不见了？fail-closed）", addPath)
	}
}
