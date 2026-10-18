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
	// manager 生产目录的全文：本条用例里多处判据都钉"某条转发不得回潮"，
	// 统一按**整个目录**扫（写死单个文件名会让文件一搬/一删就让判据 Fatal——本批踩过三次）。
	mgrAll := dirProductionBody(t, filepath.Join(root, "internal", "sessionruntime"))
	src := readFileOrFatal(t, filepath.Join(root, "internal", "sessionruntime", "service.go"))
	for _, call := range []string{"SetCapabilityFacts(s)", "SetCapabilityLookup(s)"} {
		if !strings.Contains(src, call) {
			t.Errorf("internal/sessionruntime/service.go 的 `NewService` 里找不到 `sessionPlane.%s`："+
				"能力事实/查表没有登记到会话域上。\n"+
				"漏登记的后果是**静默**的（指纹恒空 ⇒ 改了定义不换代际；或校验恒过 ⇒ 选了没生效），"+
				"而构建、启动、既有测试全都正常。", call)
		}
	}
	// capSrc 是 `svccapability` 包**全部生产文件**拼起来的正文（剥注释后）。
	//
	// 为什么按目录扫而不是钉单个文件：P4a 正文第三十九刀把那个端口文件（`capability.go`）整个
	// 删掉了，钉路径的判据会当场变成 `readFileOrFatal` 的 Fatal；而"文件被删"本身又不能作为判据
	// ——把接口换个文件名重新添回来就绕过去了。按目录扫同时解决这两件事，并且顺带把下面三批
	// "接口里不许有声明"的判据收紧成"包里任何地方都不许有"。
	capFiles := productionGoFiles(t, filepath.Join(root, "internal", "service", "svccapability"))
	if len(capFiles) == 0 {
		t.Fatal("`internal/service/svccapability` 下扫不到任何生产文件：判据失锚（目录被搬走/改名？），不许静默通过")
	}
	var capBuf strings.Builder
	for _, f := range capFiles {
		capBuf.WriteString(stripLineComments(readFileOrFatal(t, f)))
		capBuf.WriteString("\n")
	}
	capSrc := capBuf.String()

	// ——— 技能启停 / 物化现状归域平面（P4a 正文第三十六刀）———
	//
	// `SetSkillEnabled` 与 `SkillMaterialization` 的投影（两个 DTO）与错误码（`skill_not_found` 等）
	// 都在 `domain/skill` 的平面里，用例层再留一份声明就只是给"一行转发"留位置。判据取"那两行
	// 在用例层不存在"：一个方法声明在任何地方都能满足，删掉转发之后声明会**静默地**无事可做。
	// v3.1 §3.2.1：控制器不得 import domain，故 `svccapability.CapabilitySvc` **必须**声明这两条
	// （窄接口是控制器唯一入口）；下面只钉"实现仍在域平面"。
	for _, must := range []string{"func (m *Plane) SetSkillEnabled(", "func (m *Plane) SkillMaterialization("} {
		dom := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "plane.go")))
		dom += stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "materialization.go")))
		if !strings.Contains(dom, must) {
			t.Errorf("`domain/skill` 里找不到 %q：启停/物化的实现在域平面。", must)
		}
	}
	// 扫描面是**整个 manager 生产目录**（原先只读 `skills.go` 一个文件——那个文件已随第五十刀删除，
	// 写死文件名会让这条判据直接 Fatal；改成整目录之后它同时变强：那两行转发无论放回哪个文件都红）。
	if strings.Contains(mgrAll, "func (s *Service) SetSkillEnabled(") ||
		strings.Contains(mgrAll, "func (s *Service) SkillMaterialization(") {
		t.Error("`internal/sessionruntime` 里又出现技能启停/物化的转发：那两行已删（P4a 正文第三十六刀），" +
			"投影与错误码都在 `domain/skill`，控制器直接 `h.svc.*`。")
	}
	// ——— 定义**写面**六条（skill 三条 / mcp 三条）归域平面（P4a 正文第三十七刀）———
	//
	// 六条转发（`CreateSkill`/`UpdateSkill`/`DeleteSkill`/`CreateMcpServer`/`UpdateMcpServer`/
	// `DeleteMcpServer`）的函数体各自只有一行、且目标方法**名字都不同**（`WriteSkill(ctx, "", req, true)`
	// 这类「同一方法 + create 开关」的形状）——**正因为名字不同，接口方法名与域方法名的对应关系
	// 没有编译器能替我们记住**：谁把 `create` 开关写反，或者把 id 与 req 传颠倒，编译照样过。
	// 所以判据钉三件事：接口里零命中那六个名字、manager 两个薄文件里零命中那六个方法、域里四个
	// 本体（`WriteSkill`/`DeleteSkill`/`WriteServer`/`DeleteServer`）必须在（失锚＝空转）。
	// v3.1：接口必须声明写面（控制器不 import domain），但**实现**仍只在域平面。
	if !strings.Contains(capSrc, "WriteSkill(") || !strings.Contains(capSrc, "WriteServer(") {
		t.Error("`svccapability.CapabilitySvc` 缺写面方法：v3.1 起控制器只认这个接口。")
	}
	// 扫描面是**整个 manager 生产目录**（P4a 正文第五十二刀删掉了 `skills_write.go` / `mcps_write.go`
	// 两个只剩说明头的文件；写死文件名会 Fatal，而写成整目录**同时变强**：那六行转发放回任何
	// 文件都红——本批第五次踩「判据写死在某个文件名上」，处置一律如此）。
	writeFaces := mgrAll
	for _, dead := range []string{"func (s *Service) CreateSkill(", "func (s *Service) UpdateSkill(", "func (s *Service) DeleteSkill(",
		"func (s *Service) CreateMcpServer(", "func (s *Service) UpdateMcpServer(", "func (s *Service) DeleteMcpServer("} {
		if strings.Contains(writeFaces, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：技能/MCP 写面的一行转发已删"+
				"（实现全在 `domain/skill` / `domain/mcp` 的写面，控制器直接 `h.svc.*` / `h.mcps.*`）。", dead)
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
	//	④ 控制器真的接在**这份**平面上：`h.svc.Probe(` 必须出现在 `ProbeMcpServer`
	//	   的**函数体**里——少了它，调用点自己 `mcp.NewPlane(...)` 也能编译，而那份新平面
	//	   没有注册表、探测恒 40401（"接错平面"是这一族最贵的错，见 §3.1 的 3y/3η）。
	for _, dead := range []string{"ProbeMcpServer(ctx context.Context", "AutoProbeMcpServers("} {
		if strings.Contains(capSrc, dead) {
			t.Errorf("`svccapability` 的接口里又出现 `%s`：探测面归域平面（`domain/mcp.Plane.Probe`/"+
				"`.AutoProbe`），接口里留声明 = 给一行转发留位置。", dead)
		}
	}
	// 同一个理由（第五十二刀删掉了 `mcp_probe.go` 这个说明头文件）：按整目录扫。
	probeFace := mgrAll
	for _, dead := range []string{"func (s *Service) ProbeMcpServer(", "func (s *Service) AutoProbeMcpServers(",
		"func (s *Service) SetMcpProbeTimeout("} {
		if strings.Contains(probeFace, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：那三条已随 P4a 正文第三十八刀删除"+
				"（探测面走 `a.svc.Planes().Mcp` / `h.svc.Probe`）。", dead)
		}
	}
	for _, must := range []string{"func (p *Plane) Probe(", "func (p *Plane) AutoProbe("} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "mcp", "probe.go"))), must) {
			t.Errorf("`domain/mcp/probe.go` 里找不到 %q：判据失锚＝空转（方法被改名/搬走时这条判据不会红）。", must)
		}
	}
	capCtr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrcapability", "capability.go")))
	if !regexp.MustCompile(`svc\s+svccapability\.CapabilitySvc`).MatchString(capCtr) {
		t.Error("`ctrcapability` 里找不到 `svc svccapability.CapabilitySvc` 字段：控制器只认用例接口（v3.1 §3.2.1）。")
	}
	if body := funcBodyOf(t, capCtr, "func (h *capabilityCtr) ProbeMcpServer("); !strings.Contains(body, "h.svc.Probe(") {
		t.Error("`ProbeMcpServer` handler 的函数体里没有 `h.svc.Probe(`：探测面已归域平面，" +
			"经同包中间函数或另造平面都会让这条判据失去意义（前者把落点藏起来，后者接错平面）。")
	}

	// ——— MCP live 五条归域平面 + **空接口摘除**（P4a 正文第三十九刀）———
	//
	// 五条 live 面（工具调用 / 资源 / 提示词 ×3）与 `mcpLiveServer` 是这一族最后的一行转发；
	// 搬完它们 `CapabilitySvc` 就**空了**。空接口在任何地方都能满足、编译器不提醒任何调用点，
	// 所以这一刀的判据必须**同时**钉住"接口不存在"与"三处装配格不复存在"——只钉一半，另一半
	// 就是"这个空接口随时可以被重新填上"（`svcworkspace` 第三十三/三十四刀踩过的正是这个形状）。
	//
	//	① 包内零命中 `CapabilitySvc`（**含将来新增的文件**）：空接口回潮即红；
	//	② `manager/mcp_live.go` 零命中六条转发：转发回流（该文件此后只剩说明头）；
	//	③ `domain/mcp/live.go` 六个本体必须在：失锚＝空转；
	//	④ 控制器**五个 handler 的函数体**各自必须真的调 `h.svc.X(`——少了它，handler 可以
	//	   经同包中间函数取、也可以自己 `mcp.NewPlane(...)`（后者拿到的平面没有注册表）；
	//	⑤ `router.Ports` 不再有 `Capability` 那一格、`app.go` 不再有 `Capability: a.svc`——
	//	   少这一条，"端口没了但装配点还留着"会看起来一样（而端口类型已经不存在，那一格
	//	   只能靠再引入一个别名/接口来填，正是第三条硬规矩禁的形状）。
	if !strings.Contains(capSrc, "type CapabilitySvc interface {") {
		t.Error("`internal/service/svccapability` 里没有 `CapabilitySvc`：v3.1 起控制器只认这个接口。")
	}
	// 扫描面是**整个生产目录**（原 `mcp_live.go` 已删；实现痕迹换个文件放回来也要红）。
	liveFace := dirProductionBody(t, filepath.Join(root, "internal", "sessionruntime"))
	for _, dead := range []string{"func (s *Service) CallMcpTool(", "func (s *Service) ListMcpResources(",
		"func (s *Service) ReadMcpResource(", "func (s *Service) ListMcpPrompts(",
		"func (s *Service) GetMcpPrompt(", "func (s *Service) mcpLiveServer("} {
		if strings.Contains(liveFace, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：那六条已随 P4a 正文第三十九刀删除"+
				"（live 面走 `h.svc.*`，跨面集成测试走 `svc.Planes().Mcp.LiveServer`）。", dead)
		}
	}
	liveSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "mcp", "live.go")))
	for _, must := range []string{"func (p *Plane) CallTool(", "func (p *Plane) LiveServer(",
		"func (p *Plane) ListResources(", "func (p *Plane) ReadResource(",
		"func (p *Plane) ListPrompts(", "func (p *Plane) GetPrompt("} {
		if !strings.Contains(liveSrc, must) {
			t.Errorf("`domain/mcp/live.go` 里找不到 %q：判据失锚＝空转（方法被改名/搬走时这条判据不会红）。", must)
		}
	}
	for _, c := range []struct{ handler, call string }{
		{"func (h *capabilityCtr) CallMcpTool(", "h.svc.CallTool("},
		{"func (h *capabilityCtr) ListMcpResources(", "h.svc.ListResources("},
		{"func (h *capabilityCtr) ReadMcpResource(", "h.svc.ReadResource("},
		{"func (h *capabilityCtr) ListMcpPrompts(", "h.svc.ListPrompts("},
		{"func (h *capabilityCtr) GetMcpPrompt(", "h.svc.GetPrompt("},
	} {
		if body := funcBodyOf(t, capCtr, c.handler); !strings.Contains(body, c.call) {
			t.Errorf("%s 的函数体里没有 %q：live 面归域平面后控制器必须直接消费**装配点交下来的**"+
				"那份平面（经同包中间函数或另造平面都会让这条判据失去意义）。", c.handler, c.call)
		}
	}
	if !strings.Contains(capCtr, "h.svc.") {
		t.Error("`ctrcapability` 里找不到 `h.svc.`：v3.1 起控制器只持 `svccapability.CapabilitySvc` 接口。")
	}
	for _, banned := range []string{"*skill.Plane", "*mcp.Plane", "*capability.Registry"} {
		if strings.Contains(capCtr, banned) {
			t.Errorf("`ctrcapability` 里又出现 `%s`：domain/pkg 的平面不外溢到控制器（v3.1 §3.2.1 规则 1）。", banned)
		}
	}
	// v3.1：用例实例只在装配根造；控制器只收接口。
	asmSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "assembly", "services.go")))
	if !strings.Contains(asmSrc, "svccapability.NewCapability(") {
		t.Error("`internal/assembly/services.go` 里找不到 `svccapability.NewCapability(`：能力面用例必须在装配根造。")
	}
	if strings.Contains(capCtr, "svccapability.NewCapability(") {
		t.Error("`ctrcapability` 里又出现 `svccapability.NewCapability(`：用例实例只在装配根造。")
	}

	// 第四十九刀：能力/定义面的**四条一行转发**清零（`gateway_face.go` 与 `definition_forward.go`
	// 两个文件整个删除）。
	//
	// 两族都属"定义怎么被取用"这一面：网关要的那份 server 定义（未解析密钥）与密钥解析、
	// 以及定义机制的 repo / 能力目录取用。它们原先保留同名公开方法只是因为调用点拿到的是
	// `*Service`；转发删掉之后调用点直接写 `McpPlane()` / `defs`——**名字变了**，
	// 而"这段实现在哪一层"重新变得可判定。
	for _, dead := range []string{
		"func (s *Service) GatewayMcpFace(",
		"func (s *Service) ResolveMcpSecrets(",
		"func (s *Service) definitionRepoFor(",
		"func (s *Service) capabilityDirFor(",
	} {
		if strings.Contains(mgrAll, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：能力/定义面的过渡转发已随 P4a 正文第四十九刀"+
				"清零（调用点直连 `McpPlane()` / `defs`）。", dead)
		}
	}
	gwSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "gateway", "source.go")))
	for _, want := range []string{"s.planes.Mcp.GatewayFace(", "s.planes.Mcp.ResolveSecrets("} {
		if !strings.Contains(gwSrc, want) {
			t.Errorf("`internal/gateway/source.go` 里找不到 `%s`：网关装配必须直连 MCP 平面。"+
				"漏了会让这一条链悄悄退回某个中间层。", want)
		}
	}

	// 第五十刀：**技能投影登记与物化**的四条转发清零（`manager/skills.go` 整个文件随之删除）。
	//
	// 这一族的调用点分散在装配（app 的投影登记与物化）、能力事实/查表（两条）与测试，
	// 转发删掉之后一律直连 `skill.Plane` / `definition.Mechanism`。失效方式与前面几族同形：
	// 一行转发的正确性完全等价于被转发的那一行，所以它写错与否都不会让任何测试变红。
	for _, dead := range []string{
		"func (s *Service) SetSkillProjections(",
		"func (s *Service) skillProjectionFor(",
		"func (s *Service) ReconcileSkillProjection(",
		"func (s *Service) capabilityDirSnapshot(",
	} {
		if strings.Contains(mgrAll, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：技能投影面的过渡转发已随 P4a 正文第五十刀"+
				"清零（调用点直连 `SkillPlane()` / `defs`）。", dead)
		}
	}
	// P5a 起事实来自 `registry.Planes`；v3.1 起投影重校在 `internal/assembly/meta.go` 的装载辅助里。
	appAll := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "assembly", "meta.go")))
	for _, want := range []string{"as.planes.Skills.Projections().Set(", "as.planes.Skills.ReconcileSkillProjection("} {
		if !strings.Contains(appAll, want) {
			t.Errorf("`internal/assembly/meta.go` 里找不到 `%s`：装配期的投影登记与物化必须直连技能平面。"+
				"漏了会让这两条链悄悄退回某个中间层。", want)
		}
	}
}
