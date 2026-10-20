package archtest

import (
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
