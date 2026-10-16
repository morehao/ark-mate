package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P3c「能力判决唯一实现」守卫 ———
//
// 同一份"这条能力在这个 runtime 上够不够得着"的判决服务两件事：
//
//	`GET /library` 的逐 runtime applicability（告诉用户"投得过去吗"）；
//	建会话的选定校验（M3-3：不可达就报错，绝不静默丢弃）。
//
// 两处各写一遍必然漂移，而漂移的两个方向都不可接受、且**都不会让既有测试变红**：
//
//	「Library 说不可达、建会话却放行」——用户以为钉住了能力，实际没投（静默）；
//	「Library 说可达、建会话被拒」——用户没法理解为什么选不上。
//
// P3c 把判决本体（判据顺序 + 状态集 + 文案）搬进 `internal/domain/session`：
// `capability_verdict.go` 是唯一声明处，`manager/library.go` 的两条降级成薄适配。
//
// 判据三段：
//
//	① **判决本体在域内**（状态集五取值 + 判据文案锚 must 出现在 `capability_verdict.go`，
//	   且该文件必须同时声明 `SkillVerdict`/`McpVerdict` 两个入口）；
//	② **两条消费路径都真的接了域**：读面（`manager/library.go`）与校验面
//	   （`domain/session/capability_selection_validate.go`）都必须调用域判决——漏任一处的表现是
//	   "那条路自己长出一套判据"；
//	③ **装配真的接上**：`NewService` 里必须登记能力事实提供者与查表（漏登记的指纹恒空/校验恒过，
//	   两种都静默）。
//
// 反证（实跑，见计划 §3.1.48）：把 `manager/library.go` 的 `session.SkillVerdict(...)` 删掉 →
// 红；把 `NewService` 里的 `SetCapabilityLookup(s)` 删掉 → 红；改掉域内任一状态集常量 → 红。

// capabilityVerdictOwner 判决本体的归属包（相对仓库根）。
const capabilityVerdictOwner = "internal/domain/session"

// capabilityVerdictFile 判决本体的文件（相对仓库根）。
const capabilityVerdictFile = capabilityVerdictOwner + "/capability_verdict.go"

// capabilityStatusConstants 状态集的五个取值（`dtolibrary` 转出的那一套；唯一声明在
// `pkg/object/objdelivery`）。
var capabilityStatusConstants = []string{
	"StatusProjected",
	"StatusUntested",
	"StatusNotConfigured",
	"StatusNotApplicable",
	"StatusDisabled",
}

// capabilityVerdictMarkers 判决文案里的**判据锚**（不是完整句子：句子会随人话润色微调，
// 锚取的是"这条判决是关于什么"的那几个字）。
//
// 选中它们的理由：每一条都**只可能**出现在判决里（列表 WARN 用的是另一种措辞——那是"这条为什么
// 没生效"的提示，不是可达性判决），而它们恰好覆盖五种状态的每一种——少一种就说明那条判据被搬走了。
var capabilityVerdictMarkers = []string{
	"改回 enabled:true 即可恢复",
	"runtimes 白名单没有",
	"没解析出落点",
	"尚未经实测",
	"不承载",
	"经平台 MCP 网关代连",
}

// TestCapabilityVerdictLivesInSessionDomain 判决本体（状态集 + 两个入口 + 判据文案）只许声明在
// `domain/session`。
//
// fail-closed：状态集或任一文案锚不在该文件里 → 失败（常量被改名 = 判据失锚，守卫会静默空转）。
func TestCapabilityVerdictLivesInSessionDomain(t *testing.T) {
	root := backendRoot(t)
	src := stripLineComments(readFileOrFatal(t, filepath.Join(root, filepath.FromSlash(capabilityVerdictFile))))

	for _, fn := range []string{"func SkillVerdict(", "func McpVerdict("} {
		if !strings.Contains(src, fn) {
			t.Fatalf("%s 里找不到 %q：能力判决的唯一实现被改名/搬走了（守卫失锚）。", capabilityVerdictFile, fn)
		}
	}
	for _, c := range capabilityStatusConstants {
		if !strings.Contains(src, c) {
			t.Fatalf("%s 里没有状态集常量 %q：判据本体被改名/搬走了（守卫失锚）。", capabilityVerdictFile, c)
		}
	}
	for _, p := range capabilityVerdictMarkers {
		if !strings.Contains(src, p) {
			t.Fatalf("%s 里没有判据文案锚 %q：某一条可达性判据被搬走了？", capabilityVerdictFile, p)
		}
	}
}

// TestCapabilityVerdictConsumersDelegateToDomain 两条消费路径都必须调用域的判决入口。
//
// 这条钉的是**"唯一实现"这件事的另一半**：域里有唯一实现（上一条），但消费方各写一套的话，
// 唯一实现就只是"没人用的那份"。两处漏接的症状不同、但都静默：
//
//	读面漏接 → 面板显示的可达性与建会话校验各说各话；
//	校验面漏接 → 用户钉住的能力"选了没生效"（正是 M3-3 要消灭的那个失败面）。
func TestCapabilityVerdictConsumersDelegateToDomain(t *testing.T) {
	root := backendRoot(t)
	cases := []struct {
		file string
		call string
		why  string
	}{
		// P4a 正文第六刀起：读面孔径随实现搬进了用例层（`svclibrary`），判据跟着它走——
		// 留一个指向 `manager/library.go` 的锚会在文件搬走之后变成 `readFileOrFatal` 的 Fatal
		// （**这一次是实测出来的**：那正是"判据失锚不静默"该有的样子）。
		{"internal/service/svclibrary/list.go", "session.SkillVerdict(",
			"Library 读面（skill 那条）"},
		{"internal/service/svclibrary/list.go", "session.McpVerdict(",
			"Library 读面（MCP 那条）"},
		{"internal/domain/session/capability_selection_validate.go", "SkillVerdict(",
			"建会话的选定校验（skill 那条）"},
		{"internal/domain/session/capability_selection_validate.go", "McpVerdict(",
			"建会话的选定校验（MCP 那条）"},
	}
	for _, c := range cases {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(root, filepath.FromSlash(c.file))))
		if !strings.Contains(src, c.call) {
			t.Errorf("%s 里找不到 %q（%s）：这一路没有走 `%s` 里那份唯一判决——"+
				"各写一套的后果是「Library 说不可达、建会话却放行」或反过来，"+
				"而两种都**不会让既有测试变红**。", c.file, c.call, c.why, capabilityVerdictFile)
		}
	}
	// 消费面不许把判决抄回来：`svclibrary` 里出现判据文案锚即意味着有人手写了一遍
	// （它只该搬运 `session.SkillVerdict`/`McpVerdict` 的返回值）。
	libSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root,
		filepath.FromSlash("internal/service/svclibrary/list.go"))))
	for _, p := range capabilityVerdictMarkers {
		if strings.Contains(libSrc, p) {
			t.Errorf("internal/service/svclibrary/list.go 里出现判决文案锚 %q：文案随判决一起归 %s，"+
				"消费层只搬运结论（`session.SkillVerdict`/`McpVerdict` 的返回值）。", p, capabilityVerdictOwner)
		}
	}
}

// TestCapabilityWiringIsAttached 能力事实提供者与定义查表必须在 `NewService` 里真的登记。
//
// 漏登记的两种表现都**静默**：
//
//	漏 `SetCapabilityFacts` → 指纹恒空（"改了定义不换代际"：进程级配置面永远停在第一次解析的那套）；
//	漏 `SetCapabilityLookup` → 选定校验恒过（"选了没生效"：M3-3 的整条纪律失效）。
//
// 判据是调用点字面量：这一层没有运行时信号，只有"这一行还在不在"。
func TestCapabilityWiringIsAttached(t *testing.T) {
	root := backendRoot(t)
	src := readFileOrFatal(t, filepath.Join(root, "internal", "manager", "service.go"))
	for _, call := range []string{"SetCapabilityFacts(s)", "SetCapabilityLookup(s)"} {
		if !strings.Contains(src, call) {
			t.Errorf("internal/manager/service.go 的 `NewService` 里找不到 `sessionPlane.%s`："+
				"能力事实/查表没有登记到会话域上。\n"+
				"漏登记的后果是**静默**的（指纹恒空 ⇒ 改了定义不换代际；或校验恒过 ⇒ 选了没生效），"+
				"而构建、启动、既有测试全都正常。", call)
		}
	}
	// ——— 技能启停 / 物化现状归域平面（P4a 正文第三十六刀）———
	//
	// `SetSkillEnabled` 与 `SkillMaterialization` 的投影（两个 DTO）与错误码（`skill_not_found` 等）
	// 都在 `domain/skill` 的平面里，用例层再留一份声明就只是给"一行转发"留位置。判据取"那两行
	// 在用例层不存在"：一个方法声明在任何地方都能满足，删掉转发之后声明会**静默地**无事可做。
	for _, dead := range []string{"SetSkillEnabled(ctx context.Context", "SkillMaterialization(id string"} {
		if strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svccapability", "capability.go"))), dead) {
			t.Errorf("`svccapability` 的接口里又出现 `%s`：投影与错误码都在域平面（`domain/skill`），"+
				"控制器直接 `h.skills.*`（接口里留声明 = 给一行转发留位置）。", dead)
		}
	}
	if mgr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "skills.go"))); strings.Contains(mgr, "func (s *Service) SetSkillEnabled(") ||
		strings.Contains(mgr, "func (s *Service) SkillMaterialization(") {
		t.Error("`internal/manager/skills.go` 里又出现技能启停/物化的转发：那两行已删。")
	}
	// ——— 定义**写面**六条（skill 三条 / mcp 三条）归域平面（P4a 正文第三十七刀）———
	//
	// 六条转发（`CreateSkill`/`UpdateSkill`/`DeleteSkill`/`CreateMcpServer`/`UpdateMcpServer`/
	// `DeleteMcpServer`）的函数体各自只有一行、且目标方法**名字都不同**（`WriteSkill(ctx, "", req, true)`
	// 这类「同一方法 + create 开关」的形状）——**正因为名字不同，接口方法名与域方法名的对应关系
	// 没有编译器能替我们记住**：谁把 `create` 开关写反，或者把 id 与 req 传颠倒，编译照样过。
	// 所以判据钉三件事：接口里零命中那六个名字、manager 两个薄文件里零命中那六个方法、域里四个
	// 本体（`WriteSkill`/`DeleteSkill`/`WriteServer`/`DeleteServer`）必须在（失锚＝空转）。
	for _, dead := range []string{"CreateSkill(ctx context.Context", "UpdateSkill(ctx context.Context", "DeleteSkill(ctx context.Context",
		"CreateMcpServer(ctx context.Context", "UpdateMcpServer(ctx context.Context", "DeleteMcpServer(ctx context.Context"} {
		if strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svccapability", "capability.go"))), dead) {
			t.Errorf("`svccapability` 的接口里又出现 `%s`：写面归域平面（`WriteSkill`/`WriteServer`），"+
				"接口里留声明 = 给一行转发留位置。", dead)
		}
	}
	writeFaces := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "skills_write.go"))) +
		stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "mcps_write.go")))
	for _, dead := range []string{"func (s *Service) CreateSkill(", "func (s *Service) UpdateSkill(", "func (s *Service) DeleteSkill(",
		"func (s *Service) CreateMcpServer(", "func (s *Service) UpdateMcpServer(", "func (s *Service) DeleteMcpServer("} {
		if strings.Contains(writeFaces, dead) {
			t.Errorf("`internal/manager/{%s,mcps_write.go}` 里又出现 `%s`：那一行转发已删。", "skills_write.go", dead)
		}
	}
	for _, must := range []string{"func (m *Plane) WriteSkill(", "func (m *Plane) DeleteSkill("} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "write.go"))), must) {
			t.Errorf("`domain/skill/write.go` 里找不到 %q：判据失锚＝空转。", must)
		}
	}
	for _, must := range []string{"func (p *Plane) WriteServer(", "func (p *Plane) DeleteServer("} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "mcp", "write.go"))), must) {
			t.Errorf("`domain/mcp/write.go` 里找不到 %q：判据失锚＝空转。", must)
		}
	}

	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "plane.go"))), "func (m *Plane) SetSkillEnabled(") ||
		!strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "materialization.go"))), "func (m *Plane) SkillMaterialization(") {
		t.Error("`domain/skill` 里找不到两个本体：判据失锚＝空转。")
	}

	// ——— 探测面两条 + 一条死转发归域平面（P4a 正文第三十八刀）———
	//
	// `ProbeMcpServer` / `AutoProbeMcpServers` 的投影（`McpProbeResp`）与错误码
	//（`mcp_server_not_found` / 连接失败 50201）都在 `domain/mcp.Plane` 里折好，用例层再留声明
	// 就只是给"一行转发"留位置；`SetMcpProbeTimeout` 是**零调用点的死转发**（域里的
	// `SetProbeTimeout` 仍在，由域自己的测试用）。判据钉四段，缺哪一段都有一种静默失效：
	//
	//	① 接口里零命中那两条：留声明 = 给一行转发留位置（`manager` 的实现也会跟着回来）；
	//	② `manager/mcp_probe.go` 零命中三个函数：转发回流（该文件此后只剩说明头）；
	//	③ 域里两个本体必须在：失锚＝空转（方法改名/搬走时这条判据会**静默变绿**）；
	//	④ 控制器真的接在**这份**平面上：`h.mcpPlane.Probe(` 必须出现在 `ProbeMcpServer`
	//	   的**函数体**里——少了它，调用点自己 `mcp.NewPlane(...)` 也能编译，而那份新平面
	//	   没有注册表、探测恒 40401（"接错平面"是这一族最贵的错，见 §3.1 的 3y/3η）。
	for _, dead := range []string{"ProbeMcpServer(ctx context.Context", "AutoProbeMcpServers("} {
		if strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svccapability", "capability.go"))), dead) {
			t.Errorf("`svccapability` 的接口里又出现 `%s`：探测面归域平面（`domain/mcp.Plane.Probe`/"+
				"`.AutoProbe`），接口里留声明 = 给一行转发留位置。", dead)
		}
	}
	probeFace := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "mcp_probe.go")))
	for _, dead := range []string{"func (s *Service) ProbeMcpServer(", "func (s *Service) AutoProbeMcpServers(",
		"func (s *Service) SetMcpProbeTimeout("} {
		if strings.Contains(probeFace, dead) {
			t.Errorf("`internal/manager/mcp_probe.go` 里又出现 `%s`：那三条已随 P4a 正文第三十八刀删除"+
				"（探测面走 `a.svc.McpPlane()` / `h.mcpPlane`）。", dead)
		}
	}
	for _, must := range []string{"func (p *Plane) Probe(", "func (p *Plane) AutoProbe("} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "mcp", "probe.go"))), must) {
			t.Errorf("`domain/mcp/probe.go` 里找不到 %q：判据失锚＝空转（方法被改名/搬走时这条判据不会红）。", must)
		}
	}
	capCtr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrcapability", "capability.go")))
	if !regexp.MustCompile(`mcpPlane\s+\*mcp\.Plane`).MatchString(capCtr) {
		t.Error("`ctrcapability` 里找不到 `mcpPlane *mcp.Plane` 字段（容空白）：探测面由控制器直接消费域平面，" +
			"而它必须是装配点交下来的**那一份**（自己 `mcp.NewPlane(...)` 会得到一份没有注册表的平面）。")
	}
	if body := funcBodyOf(t, capCtr, "func (h *capabilityCtr) ProbeMcpServer("); !strings.Contains(body, "h.mcpPlane.Probe(") {
		t.Error("`ProbeMcpServer` handler 的函数体里没有 `h.mcpPlane.Probe(`：探测面已归域平面，" +
			"经同包中间函数或另造平面都会让这条判据失去意义（前者把落点藏起来，后者接错平面）。")
	}
}
