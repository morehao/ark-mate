package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第六刀「能力 Library 读模型归属」守卫 ———
//
// `ListLibrary`（跨四个平面的聚合读模型）从 `manager/library.go`（`*Service` 的方法）搬进
// `internal/service/svclibrary`。这一刀与上一刀（模型写面，现并入 `svcmodel`）有一处**结构上的不同**，
// 值得钉住：它**不需要过渡端口**——依赖全是事实：
//
//	*capability.Registry  三张能力表（skill/mcp/tool）
//	*skill.Plane          已解析出的技能投影（逐 runtime 的可达性候选）
//	*runtime.Registry     已注册底座 + 它声明的 MCP 传输
//	*mcp.Secrets          「密钥引用解析不出来」这类告警事实
//	func() bool           平台 MCP 网关开关（运行期可变 → 读取口，不是快照值）
//
// 四种失效各有一条判据：
//
//	① **实现回流**：谁在 `manager` 里再写一遍聚合（再 join 一次三表、再排一次序、再收口一次
//	   `applicability` 的空数组形态）——两份读模型静默分叉，同一个资源在两个入口显示不同的
//	   可达性，而两边都编译得过、都"看起来在工作"；
//	② **判决分叉**：逐 runtime 的可达性判决本体在 `domain/session`（P3c 的**唯一实现**），
//	   这一层必须**委托**它；顺手写一个本地 `switch` 就会让"面板说可达、注入时被跳过"回来；
//	③ **默认约定分叉**："未声明 MCP 传输 → 仅 stdio"有两个消费面（本读模型与注入判据），
//	   所以它必须只有一处实现（`runtime.Registry.McpTransportsFor`）；
//	④ **装配漏接**：`ctrlibrary` 忘了造用例、或 `app.go` 没交那几份事实——前者一请求就 panic，
//	   后者是**静默空转**（nil 能力面 → 恒空清单，而路由照常 200）。
//
// 反证（实跑）：把 `func (s *Service) ListLibrary(` 加回 `manager`（真实现的形态，能编译）→ 红；
// 抽掉 `ctrlibrary` 的 `svclibrary.NewLibrary(` → 红；把 `McpSecrets` 从 `router.Ports` 拿掉 → 红；
// 把 `Capabilities: a.svc.Planes().Capabilities` 从 `app.go` 抽掉 → 红；把 `manager` 里
// `s.reg.McpTransportsFor(` 换回本地的 `len(spec.McpTransports) == 0` → 红。

// libraryUseCaseAnchors 用例层的实现锚（**实现**，不是转发）。
var libraryUseCaseAnchors = []string{
	"func NewLibrary(",
	"var _ LibrarySvc = (*Library)(nil)",
	"func (l *Library) ListLibrary(",
	"func (l *Library) librarySkills(",
	"func (l *Library) libraryMcps(",
	"func (l *Library) libraryTools(",
	"func libraryMatch(",
	"func libraryOrigin(",
	"func libraryMcpDesc(",
}

// libraryVerdictAnchors 判决**委托**的锚：领域判决唯一实现的两个入口 + 五份事实的取用处。
var libraryVerdictAnchors = []string{
	"session.SkillVerdict(",
	"session.McpVerdict(",
	"l.skills.Snapshot()",
	"l.secrets.UnresolvedKeys(",
	"l.reg.McpTransportsFor(",
	"func (l *Library) mcpVerdictFacts(",
}

// libraryRelocationMarkers manager 侧**不得再出现**的实现痕迹。
var libraryRelocationMarkers = []string{
	"func (s *Service) ListLibrary(",
	"func libraryMatch(",
	"func libraryOrigin(",
	"func libraryMcpDesc(",
	"func skillWarningMessages(",
	"func skillApplicability(",
	"func mcpApplicability(",
	"func skillRuntimeVerdict(",
	"func mcpRuntimeVerdict(",
	"func (s *Service) supportedMcpTransports(",
}

func TestLibraryReadModelLivesInLibrarySvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svclibrary")

	files := goFilesUnder(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有 .go 文件：守卫失去检查对象", dir)
	}
	var body strings.Builder
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body.WriteString(stripLineComments(readFileOrFatal(t, path)))
	}
	src := body.String()
	for _, a := range append(append([]string{}, libraryUseCaseAnchors...), libraryVerdictAnchors...) {
		if !strings.Contains(src, a) {
			t.Errorf("internal/service/svclibrary 里找不到 %q：读模型的实现被改名/搬走了？"+
				"（判据挂在它身上，失锚等于空转）", a)
		}
	}
	// 这一层的依赖**全是事实**：它既不该认识过渡期的 `manager`，也不该自己去读库
	// （读模型 join 的是注册表与投影，`store` 出现在这里意味着有人把"数据面"又接了一遍）。
	for _, m := range []string{
		`"github.com/morehao/ark-mate/internal/sessionruntime"`,
		`"github.com/morehao/ark-mate/internal/persistence/store"`,
	} {
		if strings.Contains(src, m) {
			t.Errorf("internal/service/svclibrary 里出现 import %s：这一层的依赖是域/包事实"+
				"（能力注册表 / 技能平面 / 底座注册表 / 密钥面），读模型自己不读库。", m)
		}
	}

	// ① manager 侧：实现痕迹一律不得回来；文件本身也不得复活。
	mgr := filepath.Join(root, "internal", "sessionruntime")
	mgrFiles := productionGoFiles(t, mgr)
	if len(mgrFiles) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	for _, path := range mgrFiles {
		base := filepath.Base(path)
		if base == "library.go" || base == "library_read.go" {
			t.Errorf("internal/sessionruntime/%s 复活了：读模型**只有一份实现**，在 `internal/service/svclibrary`"+
				"（P4a 正文第六刀）。两份并存的后果是同一个资源在两个入口显示不同的可达性。", base)
		}
		mgrBody := stripLineComments(readFileOrFatal(t, path))
		for _, m := range libraryRelocationMarkers {
			if strings.Contains(mgrBody, m) {
				t.Errorf("internal/sessionruntime/%s 里出现读模型的实现痕迹 %q：它只有一份实现，在 "+
					"`internal/service/svclibrary`。在这里再写一遍的后果是聚合口径与可达性判决静默分叉。",
					base, m)
			}
		}
	}

	// ④ 工具目录是**同一族读模型**（P4a 正文第十二刀）：`ListTools` 从 `manager` 收进
	// `svccapability`。它没有请求校验、没有错误码出口，但**有响应装配**——`capability.Tool`
	// → `dtocapability.ToolResp` 这个投影只能落在用例层：`pkg/` 不能 import `internal/dto`
	// （分层方向），控制器又只做参数绑定与回响应。所以它不像会话那几个读模型那样"调用方直接用
	// 平面"，而是像本文件钉的 Library 读模型一样**收进 svc**。
	//
	// 失效方式与 ① 同形（实现回流 / 就地投影），只在能力面上：
	capsSvc := filepath.Join(root, "internal", "service", "svccapability")
	var capsBody strings.Builder
	for _, path := range goFilesUnder(t, capsSvc) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		capsBody.WriteString(stripLineComments(readFileOrFatal(t, path)))
	}
	capsSrc := capsBody.String()
	for _, anc := range []string{
		"type ToolCatalog interface {",
		"func NewTools(caps *capability.Registry) ToolCatalog",
		"var _ ToolCatalog = (*Tools)(nil)",
		"func (t *Tools) ListTools() ([]dtocapability.ToolResp, error)",
		"t.caps.ListTools()",
	} {
		if !strings.Contains(capsSrc, anc) {
			t.Errorf("internal/service/svccapability 里找不到 %q：工具目录读模型的实现被改名/搬走了？"+
				"（判据挂在它身上，失锚等于空转）", anc)
		}
	}
	for _, path := range mgrFiles {
		base := filepath.Base(path)
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "func (s *Service) ListTools(") {
			t.Errorf("internal/sessionruntime/%s 里又出现 `func (s *Service) ListTools(`：工具目录的实现只有一份，"+
				"在 `internal/service/svccapability`（P4a 正文第十二刀）。两份并存的后果是同一个工具"+
				"在 `/tools` 与别处的可见范围静默分叉。", base)
		}
	}
	// 取用路径：控制器**函数体**里必须经用例（同包中间函数喂不绿它）。
	capCtr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrcapability", "capability.go")))
	if !regexp.MustCompile(`svc\s+svccapability\.CapabilitySvc`).MatchString(capCtr) {
		t.Error("`ctrcapability` 里找不到 `svc svccapability.CapabilitySvc` 字段：控制器只认用例接口。")
	}
	if !strings.Contains(funcBodyOf(t, capCtr, "func (h *capabilityCtr) ListTools("), "h.svc.ListTools(") {
		t.Error("`capabilityCtr.ListTools` 的函数体里找不到 `h.svc.ListTools(`：工具目录必须走用例接口。")
	}

	// ④′ 技能目录走**域平面**（P4a 正文第十三刀）：与工具目录同族、但落点相反，这条边界
	// 必须钉住，否则下一个人为了"对称"会给它补一层转发。
	//
	// 分界线是**投影在哪一边**，v3.2 起改成两段（刀 4）：
	//   - `skill.Plane.ListSkills` 把 `capability.Skill` 折成**无 tag 的领域视图** `[]SkillView`
	//     （含 warnings 与 scope 判定）——这是投影的**本体**，只有一份；
	//   - `svccapability` 只做领域视图 → `dtocapability.SkillResp` 的**逐字段搬运**（`skillResps`），
	//     不重新判定、不增删语义。
	// 而工具目录的投影在 `pkg/` 之上（`pkg/` 不能 import `internal/dto`），只能整份落用例层。
	// 同一个域里的两面因此落点不同，这不是不一致。
	//
	// 守卫随之钉两件事：域的签名是**领域视图**（不是 DTO），用例层确实调用投影函数
	// （否则"搬运"会退化成一行透传，那正是第十一刀判定为骚操作的形状）。
	if !strings.Contains(capsSrc, "ListSkills()") {
		t.Error("`internal/service/svccapability` 里找不到 `ListSkills()`：v3.1 起控制器不 import domain，" +
			"技能目录必须经用例接口。")
	}
	if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "skill", "plane.go"))),
		"func (m *Plane) ListSkills() ([]SkillView, error)") {
		t.Error("`internal/domain/skill` 里找不到 `func (m *Plane) ListSkills() ([]SkillView, error)`：" +
			"投影的本体只有一份且在域里，且域只产**领域视图**、不产对外 DTO（判据失锚＝空转）。")
	}
	if !strings.Contains(funcBodyOf(t,
		readFileOrFatal(t, filepath.Join(root, "internal", "service", "svccapability", "capability.go")),
		"func (s *capabilitySvc) ListSkills("), "skillResps(") {
		t.Error("`capabilitySvc.ListSkills` 的函数体里找不到 `skillResps(`：领域视图必须经投影函数" +
			"转成对外 DTO，不能退化成一行透传。")
	}
	for _, path := range mgrFiles {
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "func (s *Service) ListSkills(") {
			t.Errorf("internal/sessionruntime/%s 里又出现 `func (s *Service) ListSkills(`：那一行转发已删"+
				"（P4a 正文第十三刀），读模型的调用点直接用域平面。", filepath.Base(path))
		}
	}
	if !strings.Contains(funcBodyOf(t, capCtr, "func (h *capabilityCtr) ListSkills("), "h.svc.ListSkills(") {
		t.Error("`capabilityCtr.ListSkills` 的函数体里找不到 `h.svc.ListSkills(`：技能目录必须走用例接口。")
	}

	// ④″ MCP 注册表读模型（P4a 正文第十四刀）：与 `ToolCatalog` 同族（投影在 `pkg/` 之上
	// ⇒ 落用例层），但它的输入来自**三个源**（注册表条目 / MCP 平面探测态 / 密钥面悬空体检），
	// 所以是这一域里第一个"多源折一"的读模型——顺手把 manager 那两条私有转发
	// （`mcpToolCounts`/`mcpProbeSnapshot`）也删了（它们只服务这一个调用点）。
	for _, anc := range []string{
		"type McpCatalog interface {",
		"func NewServers(caps *capability.Registry, plane *mcp.Plane, secrets *mcp.Secrets) McpCatalog",
		"var _ McpCatalog = (*Servers)(nil)",
		"func (s *Servers) ListMcpServers() ([]dtocapability.McpServerResp, error)",
		"s.plane.ToolCounts()",
		"s.plane.ProbeSnapshot()",
		"s.secrets.UnresolvedKeys(m)",
	} {
		if !strings.Contains(capsSrc, anc) {
			t.Errorf("internal/service/svccapability 里找不到 %q：MCP 注册表读模型的实现被改名/搬走了？"+
				"（判据挂在它身上，失锚等于空转）", anc)
		}
	}
	for _, path := range mgrFiles {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, dead := range []string{"func (s *Service) ListMcpServers(", "func (s *Service) mcpToolCounts(", "func (s *Service) mcpProbeSnapshot("} {
			if strings.Contains(body, dead) {
				t.Errorf("internal/sessionruntime/%s 里又出现 `%s`：MCP 注册表读模型只有一份实现，在 "+
					"`internal/service/svccapability`（P4a 正文第十四刀）；那两条私有转发已随调用点消失。",
					filepath.Base(path), dead)
			}
		}
	}
	if !strings.Contains(funcBodyOf(t, capCtr, "func (h *capabilityCtr) ListMcpServers("), "h.svc.ListMcpServers(") {
		t.Error("`capabilityCtr.ListMcpServers` 的函数体里找不到 `h.svc.ListMcpServers(`：注册表读模型必须走用例接口。")
	}

	// ③ 默认约定单点：`internal/runtime` 必须有那个导出入口，且 manager 里不许再自己判默认。
	regSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "runtime", "registry.go")))
	if !strings.Contains(regSrc, "func (r *Registry) McpTransportsFor(") {
		t.Error("internal/runtime/registry.go 里找不到 `func (r *Registry) McpTransportsFor(`：'未声明 MCP 传输 → 仅 stdio' " +
			"这条默认约定有两个消费面（Library 读模型与 MCP 注入判据），各写一遍必然漂移——" +
			"漂移的表现是「面板说可达、注入时被跳过」。")
	}
	for _, path := range mgrFiles {
		body := stripLineComments(readFileOrFatal(t, path))
		if strings.Contains(body, "len(spec.McpTransports) == 0") {
			t.Errorf("internal/sessionruntime/%s 里又自己判了一次 MCP 传输默认（`len(spec.McpTransports) == 0`）："+
				"默认约定只有一处实现——`runtime.Registry.McpTransportsFor`。", filepath.Base(path))
		}
	}

	// ④ 装配：用例实例只在 `internal/bootstrap/assembly` 造；控制器只收接口。
	asm := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "services.go")))
	if !strings.Contains(asm, "svclibrary.NewLibrary(") {
		t.Error("`internal/bootstrap/assembly/services.go` 里没有 `svclibrary.NewLibrary(`：用例必须在唯一装配根造出来。")
	}
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrlibrary", "library.go")))
	if strings.Contains(ctr, "svclibrary.NewLibrary(") {
		t.Error("`ctrlibrary` 里又出现 `svclibrary.NewLibrary(`：用例实例只在装配根造，控制器只收接口。")
	}
}
