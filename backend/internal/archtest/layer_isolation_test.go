package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— 分层重构（P3d/P2a 起）的层守卫 ———
//
// 新结构把解散 `internal/sessionruntime` 的实现按**层**落进 `internal/drivers/mechanism/*`（机制：无业务规则的
// 跨域设施）、`internal/domain/*`（领域：拥有数据与不变量）、`internal/service/svc*`（用例：API
// 面实现）。层分开之后最危险的失效方式有两种，**都不会让任何既有测试变红**：
//
//	① **反向依赖**：机制/领域层 import 上层（manager / service / controller / app）——一旦发生，
//	   被依赖的那个上层就成了"换个包名的 god 包"，下次重构又得从同一个地方拆起；
//	② **同层互相 import**：两个领域平面互相认识（`skill` ↔ `mcp` 最典型）——一个域的不变量会
//	   悄悄变成两个域的共同不变量，"谁拥有这份数据"从此没有答案。
//
// 两条判据都 **fail-closed**：扫到 0 个包即 `Fatal`。这与 `TestFrontendUiScanRootsCoverAllUi`
// 同形——目录搬走/改名后，扫描根失效的最坏表现是"守卫照样绿，只是不再检查任何东西"。

// goPackagesUnder 递归列出 dir 下**直接含 .go 文件**的包目录（跳过 testdata 与隐藏目录）。
func goPackagesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	var walk func(d string)
	walk = func(d string) {
		if hasGoFiles(d) {
			out = append(out, d)
		}
		for _, sub := range subdirs(t, d) {
			walk(sub)
		}
	}
	walk(dir)
	return out
}

// packageImportPath 把包目录换回 import 路径（守卫的错误文案与"自我识别"都要用它）。
func packageImportPath(root, dir string) string {
	return modulePrefix + filepath.ToSlash(rel(root, dir))
}

// layerScanPackages 递归列出 `internal/<roots...>` 之下所有包目录；扫到 0 个即 Fatal
// （见文件头：目录搬走/改名后守卫不许静默通过）。
func layerScanPackages(t *testing.T, root, layer string, roots ...string) []string {
	t.Helper()
	var pkgs []string
	for _, r := range roots {
		pkgs = append(pkgs, goPackagesUnder(t, filepath.Join(root, "internal", r))...)
	}
	if len(pkgs) == 0 {
		t.Fatalf("层守卫失效：%s 下扫到 0 个包（目录被搬走/改名了？守卫不许在这种情况下静默通过）", layer)
	}
	return pkgs
}

// TestMechanismMustNotImportUpward 锁定机制层的依赖面：
//
//	mechanism/* → pkg/* · internal/{store,model} · 同层机制
//
// **不得** import 任何 `internal/domain/*`（领域）或 `internal/service/*`（用例口）、
// 以及 `manager`/`app`/`router`/`controller`/`gateway` 这些上层实现与装配面。
//
// 为什么这条最要紧：机制层是被 ≥3 个域共享的那一层（`mechanism/definition` 被五类定义共用）。
// 它一旦认识某个领域类型，"共享"就没了——它会变成那个领域的附属品，而其余域只能反向 import
// 它（正是拆分前 `*Service` 的样子）。`mechanism/credential` 的 `ResolveProviderKey` 收窄成
// `(id, keyRef)` 两个入参（P3d-2h）就是照这条做的：机制层不认识 `providerDef`。
//
// 反证（实跑）：往 `mechanism/definition` 里加一行 import `internal/domain/session` → 红；
// 把扫描根指到不存在的目录 → Fatal。
func TestMechanismMustNotImportUpward(t *testing.T) {
	root := backendRoot(t)
	forbidden := []string{
		modulePrefix + "internal/domain",
		modulePrefix + "internal/service",
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/bootstrap/app",
		modulePrefix + "internal/delivery/router",
		modulePrefix + "internal/delivery/controller",
		modulePrefix + "internal/drivers/gateway",
	}
	for _, dir := range layerScanPackages(t, root, "internal/drivers/mechanism", "drivers/mechanism") {
		for _, imp := range importedByPackage(t, dir) {
			for _, bad := range forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("机制层越界依赖：%s 不应 import %s（机制层只向下依赖 pkg/*、store/model 与同层机制）",
						rel(root, dir), imp)
				}
			}
		}
	}
}

// TestDomainPackagesMustNotImportEachOther 锁定领域层的两条边界：
//
//	① 同层零互相 import（`domain/skill` 不得认识 `domain/mcp`，反之亦然）；
//	② 不得向上 import（`service/*` 的端口、`manager` 的实现、装配与传输面）。
//
// 领域层**允许**依赖 `pkg/*`、`internal/{mechanism,store,model,dto}`——机制层是它向下的支撑，
// DTO 是它对外形态的载体（v3.2 起域返回无 tag 的 `[]SkillView`，DTO 由 `svccapability` 投影）。
//
// 反证（实跑见计划 §4.1 的 N6/N7）：往 `domain/interaction` 里 import `domain/skill` → 红；
// 把 `internal/domain` 指到一个不存在的目录 → Fatal（不许静默空转）。
// domainValueLeafPackages 领域内的**叶子值包**：它们是某个域自己的类型分区，不是独立平面。
//
// 为什么需要这个例外：`store` 适配器要实现域端口，必须 import 域类型；而域自己的**包内测试**
// 又用真 SQLite 库（`testutil.OpenStore`），同包测试 import 一个 import 本包的包会触发 Go 的
// `import cycle not allowed in test`。把值类型放进域内叶子包后两边都依赖它，环就断了。
// 它的边界仍然很清楚：叶子包**不得** import 任何 `internal/domain` 包（下面的扫描对它同样生效）。
var domainValueLeafPackages = []string{
	modulePrefix + "internal/domain/session/queue",
	modulePrefix + "internal/domain/session/memory",
	modulePrefix + "internal/domain/session/run",
	modulePrefix + "internal/domain/session/message",
	modulePrefix + "internal/domain/session/event",
	modulePrefix + "internal/domain/session/sessionrow",
	modulePrefix + "internal/domain/session/usage",
	modulePrefix + "internal/domain/workspace/row",
	modulePrefix + "internal/domain/provider/row",
	modulePrefix + "internal/domain/automation/row",
}

func isDomainValueLeaf(imp string) bool {
	for _, leaf := range domainValueLeafPackages {
		if imp == leaf {
			return true
		}
	}
	return false
}

func TestDomainPackagesMustNotImportEachOther(t *testing.T) {
	root := backendRoot(t)
	// 叶子包必须真实存在（删了/改名了要同步这份清单，否则例外变成静默空转）。
	for _, leaf := range domainValueLeafPackages {
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(leaf, modulePrefix))); err != nil {
			t.Fatalf("领域叶子值包 %s 不存在：请同步 domainValueLeafPackages（fail-closed）", leaf)
		}
	}
	upward := []string{
		modulePrefix + "internal/service",
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/bootstrap/app",
		modulePrefix + "internal/delivery/router",
		modulePrefix + "internal/delivery/controller",
		modulePrefix + "internal/drivers/gateway",
	}
	for _, dir := range layerScanPackages(t, root, "internal/domain", "domain") {
		self := packageImportPath(root, dir)
		for _, imp := range importedByPackage(t, dir) {
			if strings.HasPrefix(imp, modulePrefix+"internal/domain") && imp != self && !isDomainValueLeaf(imp) {
				t.Errorf("同层互相依赖：%s 不应 import %s（领域平面之间必须零 import：一份数据只有一个所有者）",
					rel(root, dir), imp)
			}
			for _, bad := range upward {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("领域层越界依赖：%s 不应 import %s（领域层只向下依赖机制层与 pkg/*）",
						rel(root, dir), imp)
				}
			}
		}
	}
}

// TestServicePortsMustNotImportEachOther 锁定用例层的同层纪律：`service/svc*` 之间零互相 import。
//
// 用例层的分界是两条：① "谁调谁"（`svcsession` 与 `svccapability` 不该互相调：那是编排，归
// registry 或各自的下游领域）② "谁拥有端口类型"（`svclibrary.Query` 的类型该下沉到 `domain/skill`，
// 见 P4a）。一个 `svc` import 另一个 `svc` 会让这两条同时失效；而端口层互相 import 通常还伴随
// import 环——只是 Go 要等到某个具体调用点才报出来。
func TestServicePortsMustNotImportEachOther(t *testing.T) {
	root := backendRoot(t)
	for _, dir := range layerScanPackages(t, root, "internal/service", "service") {
		self := packageImportPath(root, dir)
		for _, imp := range importedByPackage(t, dir) {
			if strings.HasPrefix(imp, modulePrefix+"internal/service/") && imp != self {
				t.Errorf("同层互相依赖：%s 不应 import %s（用例层按 API 领域分包，互不 import）",
					rel(root, dir), imp)
			}
		}
	}
}

// TestInnerLayersUseNoTableRows 内圈（领域 / 用例 / 执行面 / 入站适配 / 装配）不得引用表行形状
// （`persistence/model`）。
//
// 层表只判**方向**：L2 → L0 是"向内"，所以 `domain` import `model` 在层表里一直是**合法**的
// ——C1 之前域的端口签名里全是 `model.*Entity`，没有任何判据会说那不对；换一份存储实现就要
// 先改一遍域，而这正是"端口语言来自外圈"的那条裂缝。C1 把 16 个实体的行形状搬进域内叶子值包
// （`domain/<agg>/row` 这一类），行↔值映射只留在 `persistence/store`。这条按**角色**把结果钉住：
// **行形状只属于持久层**，内圈任何一层都不许再把它写进签名或字段。
//
// 扫描根是**五层**而不是只有 domain：只钉 domain 的话，"把行形状挪进 svc / sessionruntime 再
// 传下去"照样绿——而那也是内圈。每个根都必须存在（目录改名/搬走即失败）。
//
// 只扫**生产** import（`pkg.Imports`）：内圈的包内测试会直插行来构造夹具（真 SQLite），
// 那恰好是"行↔值映射在适配器里"的验证方式；把测试也禁掉只会逼夹具绕路，不会让内圈更干净。
func TestInnerLayersUseNoTableRows(t *testing.T) {
	root := backendRoot(t)
	modelPkg := modulePrefix + "internal/persistence/model"
	innerRoots := []string{
		filepath.Join("internal", "domain"),
		filepath.Join("internal", "service"),
		filepath.Join("internal", "sessionruntime"),
		filepath.Join("internal", "delivery"),
		filepath.Join("internal", "bootstrap"),
	}
	scanned := 0
	for _, relRoot := range innerRoots {
		dir := filepath.Join(root, relRoot)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("内圈扫描根 %s 不存在（fail-closed）：目录被改名/搬走了？", relRoot)
		}
		for _, pkgDir := range goPackagesUnder(t, dir) {
			pkg, err := build.ImportDir(pkgDir, 0)
			if err != nil {
				t.Fatalf("解析包 %s 失败: %v", rel(root, pkgDir), err)
			}
			scanned++
			for _, imp := range pkg.Imports {
				if imp == modelPkg {
					t.Errorf("%s 不应 import %s：内圈要自持聚合与值类型，行形状只属于持久层"+
						"（把该类型搬进域内叶子值包，让 store 做行↔值映射）", rel(root, pkgDir), imp)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("内圈没有扫到任何包（fail-closed）：目录被改名/搬走了？")
	}
}

// protocolFreePackages 已经完成「协议边界」迁移的**内圈包**：它们的生产 import 里不得再出现
// `infra/acp`（协议类型只出现在连接边界与出站适配器）。
//
// 这份清单是**逐步增长的收口表**：C2 每完成一块（事件与轮次 → 错误构造 → 权限/elicitation →
// 配置项 → 连接与生命周期），就在这里登记相关包。等全部登记完，`internal/domain` 前缀可以整体
// 收成一条判据（那时才允许写「domain 不得 import acp」的全量守卫）。
//
// 为什么现在就登记：已完成的边界最容易**静默回退**——下一个人为了省一次转换，在域里直接写
// `acp.PermissionRequest` 能编译、能跑，测试也绿（翻译层还在，两条路都通），于是"域不认识协议"
// 这件事悄悄退回起点，而没有任何判据会响。
//
// 值层的两个包（`core/definition` / `core/automation`）也在表里：它们的错误构造改用 `code` 之后
// 就不该再认识协议——`core/code` 是唯一例外（协议错误 → 平台视图的转换点，按 B4 随连接边界搬走）。
var protocolFreePackages = []string{
	modulePrefix + "internal/domain/interaction",
	modulePrefix + "internal/domain/automation",
	modulePrefix + "internal/domain/workspace",
	modulePrefix + "internal/domain/skill",
	modulePrefix + "internal/domain/mcp",
	modulePrefix + "internal/domain/identity",
	modulePrefix + "internal/domain/provider",
	modulePrefix + "internal/core/definition",
	modulePrefix + "internal/core/automation",
}

// TestProtocolFreePackagesStayClean 已收口的内圈包不得再 import 协议库。
//
// fail-closed：登记了的包必须真实存在（目录改名/搬走后不许静默空转）。
func TestProtocolFreePackagesStayClean(t *testing.T) {
	root := backendRoot(t)
	acpPkg := modulePrefix + "internal/infra/acp"
	for _, pkgPath := range protocolFreePackages {
		dir := filepath.Join(root, strings.TrimPrefix(pkgPath, modulePrefix))
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("协议无关的包 %s 不存在：请同步 protocolFreePackages（fail-closed）", pkgPath)
		}
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("解析包 %s 失败: %v", pkgPath, err)
		}
		for _, imp := range pkg.Imports {
			if imp == acpPkg {
				t.Errorf("%s 已登记为「协议无关」，却仍 import %s：改用内圈值类型，翻译放连接边界",
					pkgPath, imp)
			}
		}
	}
}
