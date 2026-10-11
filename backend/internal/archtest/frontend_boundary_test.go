package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是**前端侧**的依赖边界守卫（V11）。
//
// 为什么用 Go 读前端源码：本仓前端零测试框架（无 vitest/jest/playwright），既有先例就是
// "Go 读 TS 源码"做一致性断言（TestFrontendEventTypesMatchRegistry、TestFrontendBizCodesMatchRegistry）。
// 这里沿用同一模式，不向前端引入任何测试依赖（C12）。
//
// 四条断言对应设计 §2.1 的四条依赖边界——它们把"两端功能完全一致"从**纪律**变成**结构**：
// 前端产品在类型与依赖层面就够不到 Electron API，只能走宿主能力面。

// frontendRoot 返回 frontend/ 目录（本包位于 backend/internal/archtest）。
func frontendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(backendRoot(t), "..", "frontend"))
	if err != nil {
		t.Fatalf("解析 frontend 根目录失败: %v", err)
	}
	return root
}

// frontendSrcExts 是参与边界扫描的源码后缀（含 JS：前端包里混进 .js 也要被扫到）。
var frontendSrcExts = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

// skipWalkDir 报告目录名是否属于"生成物 / 依赖"面，遍历时整棵跳过。
func skipWalkDir(name string) bool {
	switch name {
	case "node_modules", "dist", ".vite", ".git", "release", "resources":
		return true
	}
	return false
}

// listFrontendSources 返回 root 下全部前端源码文件（相对 root 的路径 + 绝对路径）。
// 目录不存在时返回错误：**目录没了 = 守卫失去对象**，不能静默变绿。
func listFrontendSources(t *testing.T, root string) []string {
	t.Helper()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("前端源码目录不存在：%s（%v）——守卫没有可检查的对象", root, err)
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if frontendSrcExts[filepath.Ext(d.Name())] {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s 下没有任何源码文件——目录结构变了？守卫形同虚设", root)
	}
	return out
}

// violator 一条违规：文件（相对仓库根）+ 行号 + 该行内容。
type violator struct {
	Rel  string
	Line int
	Text string
}

func (v violator) String() string {
	return fmt.Sprintf("%s:%d: %s", v.Rel, v.Line, strings.TrimSpace(v.Text))
}

// scanSources 对每个源码文件逐行跑 pattern，收集违规（同一文件同一行只报一次）。
func scanSources(t *testing.T, repoRoot string, files []string, pattern *regexp.Regexp, allow func(line string) bool) []violator {
	t.Helper()
	var out []violator
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", f, err)
		}
		rel, err := filepath.Rel(repoRoot, f)
		if err != nil {
			rel = f
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !pattern.MatchString(line) {
				continue
			}
			if allow != nil && allow(line) {
				continue
			}
			out = append(out, violator{Rel: rel, Line: i + 1, Text: line})
		}
	}
	return out
}

func reportViolations(t *testing.T, headline string, vs []violator) {
	t.Helper()
	if len(vs) == 0 {
		return
	}
	lines := make([]string, 0, len(vs))
	for _, v := range vs {
		lines = append(lines, "  "+v.String())
	}
	t.Errorf("%s（%d 处）：\n%s", headline, len(vs), strings.Join(lines, "\n"))
}

// —— B1：产物包纪律（apps/web 对外只有产物，没有源码，也没有运行期依赖）——

// TestWebProductPackageDiscipline 断言 `frontend/apps/web/package.json` 满足设计 §2.1 的
// "产物包而非应用"：`dependencies` 为空（依赖在构建期全部打进 dist）、`exports` 只开产物
// （绝不出现 `src`）、`files` 只发 dist 并排除 sourcemap、且**非 private**（它要随桌面运行时
// 闭包一起发布，桌面包与核心都从运行时依赖树里的同一份读）。
func TestWebProductPackageDiscipline(t *testing.T) {
	manifest := filepath.Join(frontendRoot(t), "apps", "web", "package.json")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("读 %s 失败: %v（产物包被改名/移动了？）", manifest, err)
	}
	var pkg struct {
		Private      bool              `json:"private"`
		Dependencies map[string]string `json:"dependencies"`
		Exports      map[string]any    `json:"exports"`
		Files        []string          `json:"files"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("解析 %s 失败: %v", manifest, err)
	}

	if len(pkg.Dependencies) != 0 {
		t.Errorf("apps/web 的 dependencies 必须为空（依赖在构建期打进 dist），实际有 %d 项：%v", len(pkg.Dependencies), pkg.Dependencies)
	}
	// exports 的存在性本身就是断言：缺了它 = 包对外暴露的是源码入口。
	if len(pkg.Exports) == 0 {
		t.Error("apps/web 的 exports 缺失：产物包必须只开产物入口（./dist/* 与 ./package.json）")
	}
	if _, ok := pkg.Exports["./dist/*"]; !ok {
		t.Errorf("apps/web 的 exports 必须包含 ./dist/*，实际：%v", pkg.Exports)
	}
	for _, s := range collectJSONStrings(pkg.Exports) {
		if strings.Contains(s, "src") {
			t.Errorf("apps/web 的 exports 出现源码入口 %q：产物包不得对外暴露源码", s)
		}
	}
	if pkg.Private {
		t.Error("apps/web 不得是 private：它要随桌面运行时闭包一起发布（设计 §2.1 产物包纪律）")
	}
	if !containsString(pkg.Files, "dist") {
		t.Errorf("apps/web 的 files 必须包含 dist，实际：%v", pkg.Files)
	}
	excludesMaps := false
	for _, f := range pkg.Files {
		if strings.HasPrefix(f, "!") && strings.Contains(f, ".map") {
			excludesMaps = true
		}
	}
	if !excludesMaps {
		t.Errorf("apps/web 的 files 必须显式排除 sourcemap（如 !dist/**/*.map），实际：%v", pkg.Files)
	}
}

// collectJSONStrings 递归收集 JSON 值里的全部字符串（exports 允许嵌套条件导出）。
func collectJSONStrings(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, t)
	case map[string]any:
		for _, val := range t {
			out = append(out, collectJSONStrings(val)...)
		}
	case []any:
		for _, val := range t {
			out = append(out, collectJSONStrings(val)...)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// —— B2：桌面产品只消费前端产物，不引前端源码 ——

// importPrefix 匹配一条模块引入的前缀，三种写法都要覆盖：
// `from 'x'`（普通 import / export from）、`import 'x'`（副作用引入）、`import('x')` / `require('x')`。
// 只写 `from|import(|require(` 会漏掉 `import 'electron'` 这种纯副作用引入——自证时踩到过。
const importPrefix = `(?:from\s+|import\s*\(\s*|import\s+|require\s*\(\s*)`

// specifierImport 匹配一条模块引入并捕获其 specifier。
var specifierImport = regexp.MustCompile(importPrefix + `['"]([^'"]+)['"]`)

// uiPackages 是"界面包"的名字。桌面侧引它们 = 把界面源码拉进桌面构建（只允许按路径读 dist 产物）。
//
// `@ark-mate/console` 与 `@ark-mate/ui` 是 P5（2026-10-11）之后才**变得可达**的：界面搬进
// `packages/console` 之前，它只存在于 `apps/web/src` 里，桌面侧想引也没有包名可引。搬迁让"引界面源码"
// 从"不可能"变成"一行 import"，所以这条判据必须同步扩大——否则新出现的这条路没人看着。
var uiPackages = map[string]bool{
	"@ark-mate/web":     true,
	"@ark-mate/console": true,
	"@ark-mate/ui":      true,
}

// TestDesktopMustNotImportWebSource 断言 `frontend/apps/desktop/src/**` **不**引任何界面包
// （`@ark-mate/web` / `@ark-mate/console` / `@ark-mate/ui`）。
//
// 桌面产品依赖的是 `apps/web/dist` 这份**产物**（壳按路径读静态文件、核心按 `--web-dir` 读同一目录），
// 一旦有人 `from '@ark-mate/console'`，就是把界面源码拉进桌面构建：产物与源码两份开始漂移，
// 而"两端同一份 UI"正是 R1 的实现方式。按路径读 dist 不在本断言范围（那是允许的）。
func TestDesktopMustNotImportWebSource(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")
	files := listFrontendSources(t, filepath.Join(frontendRoot(t), "apps", "desktop", "src"))
	vs := scanSources(t, repoRoot, files, specifierImport, func(line string) bool {
		m := specifierImport.FindStringSubmatch(line)
		return m == nil || !uiPackages[m[1]]
	})
	reportViolations(t, "apps/desktop 引了界面包（@ark-mate/web / @ark-mate/console / @ark-mate/ui）——只允许按路径读它的 dist 产物", vs)
}

// —— B3：桌面能力不得渗进共享 UI ——

// webForbiddenImport 匹配共享 UI 里**不允许出现**的模块引入：electron、node: 内置、@ark-mate/desktop。
//
// 只匹配"引入语句"而不是裸词，是为了不误伤注释里正常提到 Electron 的文字；
// `@ark-mate/desktop` 用**完整 specifier**（`'@ark-mate/desktop'`）而不是前缀——`@ark-mate/desktop-contract`
// 是**允许**被共享 UI 消费的（M1-8 的载体接入正是用它，它是零 Electron 引用的纯契约包）。
var webForbiddenImport = regexp.MustCompile(importPrefix + `['"](electron|node:[^'"]+|@ark-mate/desktop)['"]`)

// windowElectron 匹配"嗅探 Electron"的历史写法（设计 §10.1 明令禁止：不得读 window.electron）。
//
// 这是本文件里**唯一**的裸词模式（其余三条都只匹配引入语句），因此要给注释留出豁免：
// 设计文档原文里就写着这个词，注释引用它不该红——豁免由 commentLine 提供，代码里真写了照样红。
var windowElectron = regexp.MustCompile(`window\s*\.\s*electron`)

// commentLine 报告该行看着像注释（`//` 行、`/*` 起始行、块注释的续行 `*`）。
func commentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*")
}

// TestWebUiMustNotReachDesktopApis 断言**全部 UI 源码根**（`uiSrcRoots()`：apps/web + packages/console +
// packages/ui）都够不到桌面能力：
// 不引 `electron`、不引 `node:*` 内置模块、不引 `@ark-mate/desktop`、不嗅探 `window.electron`。
//
// 这条是"两端功能完全一致"的结构性保证：共享 UI 在**类型与依赖层面**就拿不到桌面 API，
// 于是不存在"只有桌面能跑"的分支；桌面多出来的能力只能经 `GET /v1/mate/capabilities`（声明面）
// 与 `__MATE_DESKTOP__`（执行面，存在性探测降级）进入，且浏览器端必须有服务端等价路径（V3）。
func TestWebUiMustNotReachDesktopApis(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")
	files := listUISources(t) // 搬迁后必须靠 uiSrcRoots()：写死一个目录会让这条判据静默空转
	vs := scanSources(t, repoRoot, files, webForbiddenImport, nil)
	vs = append(vs, scanSources(t, repoRoot, files, windowElectron, commentLine)...)
	reportViolations(t, "界面源码出现桌面/Node 专属引用（共享 UI 只能走宿主能力面）", vs)
}

// —— B4：能力层不得反向依赖产品层 ——

// relativeImport 捕获相对引入的 specifier（以 . 开头）。能力层只能用包内相对路径与 workspace 包名。
var relativeImport = regexp.MustCompile(importPrefix + `['"](\.[^'"]*)['"]`)

// TestPackagesMustNotImportApps 断言 `frontend/packages/*/src/**` 不出现**指到 apps/ 的相对引入**。
//
// 依赖方向是"能力层被产品层消费"，反过来就会出现：能力层里的组件直接 import 某个产品包的源码，
// 于是抽出复用/换产品时全部连坐，且 `apps/web` 的依赖图不再封闭。产品层要用能力层就按包名引
// （`@ark-mate/ui` 等），凭相对路径反向穿透一律拒绝。
func TestPackagesMustNotImportApps(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")
	packagesDir := filepath.Join(frontendRoot(t), "packages")
	entries, err := os.ReadDir(packagesDir)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", packagesDir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		src := filepath.Join(packagesDir, e.Name(), "src")
		if _, err := os.Stat(src); err != nil {
			continue // 没有 src 的包（如 @ark-mate/tsconfig）不参与
		}
		files = append(files, listFrontendSources(t, src)...)
	}
	if len(files) == 0 {
		t.Fatal("frontend/packages/*/src 下没扫到任何源码——目录结构变了？守卫形同虚设")
	}
	vs := scanSources(t, repoRoot, files, relativeImport, func(line string) bool {
		m := relativeImport.FindStringSubmatch(line)
		return m == nil || !strings.Contains(m[1], "apps/")
	})
	reportViolations(t, "能力层（packages/*）出现指向产品层（apps/）的相对引入", vs)
}

// uiSrcRoots 是**前端 UI 源码根的唯一事实源**（P5，2026-10-11）。
//
// 为什么要有它：界面原先全在 `apps/web/src` 里，六个守卫（这条的 B2/B3、能力消费者、`import.meta.env`、
// 目录选择器、设置面两条、toast 层号）都把扫描根指到那一个目录。把界面搬进 `packages/console/src`
// 之后，那些守卫会**静默失去检查对象**——`apps/web/src` 目录还在（只剩入口），"目录不存在即 fatal"
// 那条兜底根本不会触发，于是"UI 够不到桌面 API"这类判据变成空转。所以扫描根收进这一个函数：
// 搬迁只需改这里，各守卫不会各自漂移（`TestFrontendUiScanRootsCoverAllUi` 钉住"没有漏掉的 UI"）。
func uiSrcRoots(t *testing.T) []string {
	t.Helper()
	root := frontendRoot(t)
	rels := []string{
		filepath.Join("apps", "web", "src"),
		filepath.Join("packages", "console", "src"),
		filepath.Join("packages", "ui", "src"),
	}
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("UI 源码根 %s 不存在（%v）——搬迁/改名后必须同步 uiSrcRoots，守卫不接受静默失去对象", rel, err)
		}
		out = append(out, abs)
	}
	return out
}

// listUISources 返回全部 UI 源码文件（每个声明根都要非空，见 listFrontendSources 的 fail-closed）。
func listUISources(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range uiSrcRoots(t) {
		out = append(out, listFrontendSources(t, root)...)
	}
	if len(out) == 0 {
		t.Fatal("UI 源码根里一个文件都没有——目录结构变了？守卫形同虚设")
	}
	return out
}

// uiishImport 命中"这个文件是界面代码"的引入（React / 共享 UI 包 / 状态库 / antd）。
// 只看引入语句，不看裸词，避免把注释里提到 React 的纯工具文件误判成界面。
var uiishImport = regexp.MustCompile(`(?:from\s+|import\s*\()` + `['"](?:react|react-dom[^'"]*|@ark-mate/ui|@ark-mate/console|zustand|antd[^'"]*)['"]`)

// TestFrontendUiScanRootsCoverAllUi 断言**界面代码不会落在声明根之外**。
//
// 这条是 P5 的直接产物：搬迁把界面的位置变成了"可配置的东西"，而配置错了的表现是**别的守卫静默空转**
// （不会红、只是不再检查任何界面文件）。判据：`frontend/{apps,packages}/*/src` 下每个 `.tsx`、
// 或每个引入 React/共享 UI 包的 `.ts`，都必须落在 `uiSrcRoots()` 的某个根里；落在外面即红，
// 并提示"把它的根加进 uiSrcRoots"。
//
// 反证：把 `packages/console/src` 从 uiSrcRoots 里删掉 → 红（列出上千个越界文件）；
// 新建一个含 .tsx 的包而不登记 → 红。
func TestFrontendUiScanRootsCoverAllUi(t *testing.T) {
	root := frontendRoot(t)
	declared := uiSrcRoots(t)
	insideDeclared := func(path string) bool {
		for _, r := range declared {
			if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}

	var checked, outside []string
	for _, top := range []string{"apps", "packages"} {
		entries, err := os.ReadDir(filepath.Join(root, top))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", top, err)
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			src := filepath.Join(root, top, e.Name(), "src")
			if _, err := os.Stat(src); err != nil {
				continue
			}
			for _, f := range listFrontendSources(t, src) {
				ext := filepath.Ext(f)
				isUI := ext == ".tsx" || ext == ".jsx"
				if !isUI {
					if raw, err := os.ReadFile(f); err == nil && uiishImport.MatchString(string(raw)) {
						isUI = true
					}
				}
				if !isUI {
					continue
				}
				rel, _ := filepath.Rel(root, f)
				checked = append(checked, rel)
				if !insideDeclared(f) {
					outside = append(outside, rel)
				}
			}
		}
	}
	if len(checked) == 0 {
		t.Fatal("没有扫到任何界面文件——判据本身失效了（目录结构变了？）")
	}
	if len(outside) > 0 {
		show := outside
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf("有 %d 个界面文件落在 uiSrcRoots() 之外（前 %d 个）：\n  %s\n"+
			"这类漏登记的后果是**别的守卫静默空转**（不红、只是不再检查这些文件）。"+
			"把它们的根加进 uiSrcRoots()。", len(outside), len(show), strings.Join(show, "\n  "))
	}
	t.Logf("界面文件覆盖检查：%d 个文件全部落在 %d 个声明根内", len(checked), len(declared))
}
