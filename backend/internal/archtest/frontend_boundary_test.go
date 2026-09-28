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

// TestDesktopMustNotImportWebSource 断言 `frontend/apps/desktop/src/**` **不**引 `@ark/web`。
//
// 桌面产品依赖的是 `apps/web/dist` 这份**产物**（壳按路径读静态文件、核心按 `--web-dir` 读同一目录），
// 一旦有人 `from '@ark/web'`，就是把前端源码拉进桌面构建：产物与源码两份开始漂移，
// 而"两端同一份 UI"正是 R1 的实现方式。按路径读 dist 不在本断言范围（那是允许的）。
func TestDesktopMustNotImportWebSource(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")
	files := listFrontendSources(t, filepath.Join(frontendRoot(t), "apps", "desktop", "src"))
	vs := scanSources(t, repoRoot, files, specifierImport, func(line string) bool {
		m := specifierImport.FindStringSubmatch(line)
		return m == nil || m[1] != "@ark/web"
	})
	reportViolations(t, "apps/desktop 引了前端产品包 @ark/web（只允许按路径读它的 dist 产物）", vs)
}

// —— B3：桌面能力不得渗进共享 UI ——

// webForbiddenImport 匹配共享 UI 里**不允许出现**的模块引入：electron、node: 内置、@ark/desktop。
//
// 只匹配"引入语句"而不是裸词，是为了不误伤注释里正常提到 Electron 的文字；
// `@ark/desktop` 用**完整 specifier**（`'@ark/desktop'`）而不是前缀——`@ark/desktop-contract`
// 是**允许**被共享 UI 消费的（M1-8 的载体接入正是用它，它是零 Electron 引用的纯契约包）。
var webForbiddenImport = regexp.MustCompile(importPrefix + `['"](electron|node:[^'"]+|@ark/desktop)['"]`)

// windowElectron 匹配"嗅探 Electron"的历史写法（设计 §10.1 明令禁止：不得读 window.electron）。
var windowElectron = regexp.MustCompile(`window\s*\.\s*electron`)

// TestWebUiMustNotReachDesktopApis 断言 `frontend/apps/web/src/**` 够不到桌面能力：
// 不引 `electron`、不引 `node:*` 内置模块、不引 `@ark/desktop`、不嗅探 `window.electron`。
//
// 这条是"两端功能完全一致"的结构性保证：共享 UI 在**类型与依赖层面**就拿不到桌面 API，
// 于是不存在"只有桌面能跑"的分支；桌面多出来的能力只能经 `GET /v1/forge/capabilities`（声明面）
// 与 `__FORGE_DESKTOP__`（执行面，存在性探测降级）进入，且浏览器端必须有服务端等价路径（V3）。
func TestWebUiMustNotReachDesktopApis(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")
	files := listFrontendSources(t, filepath.Join(frontendRoot(t), "apps", "web", "src"))
	vs := scanSources(t, repoRoot, files, webForbiddenImport, nil)
	vs = append(vs, scanSources(t, repoRoot, files, windowElectron, nil)...)
	reportViolations(t, "apps/web 出现桌面/Node 专属引用（共享 UI 只能走宿主能力面）", vs)
}

// —— B4：能力层不得反向依赖产品层 ——

// relativeImport 捕获相对引入的 specifier（以 . 开头）。能力层只能用包内相对路径与 workspace 包名。
var relativeImport = regexp.MustCompile(importPrefix + `['"](\.[^'"]*)['"]`)

// TestPackagesMustNotImportApps 断言 `frontend/packages/*/src/**` 不出现**指到 apps/ 的相对引入**。
//
// 依赖方向是"能力层被产品层消费"，反过来就会出现：能力层里的组件直接 import 某个产品包的源码，
// 于是抽出复用/换产品时全部连坐，且 `apps/web` 的依赖图不再封闭。产品层要用能力层就按包名引
// （`@ark/ui` 等），凭相对路径反向穿透一律拒绝。
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
			continue // 没有 src 的包（如 @ark/tsconfig）不参与
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
