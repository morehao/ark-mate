package archtest

// G1 · 左栏不得有与「新建会话」重复的入口（十项修正第 1 条）。
//
// `/sessions` 曾经同时是一级导航项「问答」与左栏顶部「新建会话」的目的地——两个入口做同一件事。
// 判据落在**事实源**上，而不是某一段渲染代码。
//
// WP7（一期 A+Slot）把左栏的静态数组收敛到**统一注册表** `console/appRegistry.ts`：
// `NAV_ENTRIES` 不再是手写数组，而是 `= buildNavEntries()` 从注册表派生（`resolveAppPages()`）。
// 于是判据相应改为：
//
//	① `export const NAV_ENTRIES` 仍必须存在，且**明确由注册表派生**（`buildNavEntries` /
//	   `resolveAppPages` 之一必须在同一行出现）——改名/绕开注册表手写数组即红（fail-closed）。
//	② 注册表源 `console/appRegistry.ts` 的页面定义里**不得出现以 `/sessions` 为 route 的入口**
//	   （内置 `BUILTIN_APP_PAGES` 与动态补登都经它；它是"以会话页为目的地的一级导航项"的唯一形态）。
//	③ `NavRail` 仍保留 `.fg-rail-new`（唯一入口必须存在），且不再有 `entry.id === 'chat'` 计数分支。
//
// 反证（实跑）：把一条 route='/sessions' 的页注册进注册表 → ② 红。
//
// 注意：navView.tsx 里 `/sessions` 仍会出现在**非导航**语境（`railContextOf` 的会话区判定、
// 注释）——故②只扫注册表源 appRegistry.ts，不扫 navView 全文（后者不是"导航条目"）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavHasNoDuplicateNewSessionEntry(t *testing.T) {
	consoleSrc := filepath.Join(filepath.Dir(backendRoot(t)), "frontend", "packages", "console", "src")

	navRaw, err := os.ReadFile(filepath.Join(consoleSrc, "console", "navView.tsx"))
	if err != nil {
		t.Fatalf("读 navView.tsx 失败: %v（判据失去对象）", err)
	}
	navSrc := stripTSComments(string(navRaw))

	const decl = "export const NAV_ENTRIES"
	start := strings.Index(navSrc, decl)
	if start < 0 {
		t.Fatalf("解析不到 %q——导航条目的唯一事实源改名/搬迁了，本判据会静默空转", decl)
	}
	// 取该声明所在行（到下一个换行），确认它由注册表派生——而不是又退回手写数组。
	lineEnd := strings.Index(navSrc[start:], "\n")
	if lineEnd < 0 {
		t.Fatalf("%q 声明行解析不到——判据无法切片", decl)
	}
	declLine := navSrc[start : start+lineEnd]
	if !strings.Contains(declLine, "buildNavEntries") && !strings.Contains(declLine, "resolveAppPages") {
		t.Fatalf("%q 未从注册表派生（应引用 buildNavEntries / resolveAppPages）：`%s`", decl, strings.TrimSpace(declLine))
	}

	// ② 注册表源不得有以 /sessions 为 route 的页面定义（fail-closed：读不到即红）。
	regRaw, err := os.ReadFile(filepath.Join(consoleSrc, "console", "appRegistry.ts"))
	if err != nil {
		t.Fatalf("读 appRegistry.ts 失败: %v（注册表迁移后判据失去对象）", err)
	}
	regSrc := stripTSComments(string(regRaw))
	if strings.Contains(regSrc, "route: '/sessions'") || strings.Contains(regSrc, `route: "/sessions"`) {
		t.Errorf("注册表里有 route 为 /sessions 的页面入口——它与「新建会话」重复（需求 1）")
	}

	railRaw, err := os.ReadFile(filepath.Join(consoleSrc, "components", "NavRail.tsx"))
	if err != nil {
		t.Fatalf("读 NavRail.tsx 失败: %v", err)
	}
	rail := stripTSComments(string(railRaw))
	if !strings.Contains(rail, "fg-rail-new") {
		t.Errorf("NavRail 里找不到 fg-rail-new——唯一的「新建会话」入口不见了")
	}
	if strings.Contains(rail, "entry.id === 'chat'") {
		t.Errorf("NavRail 里仍有 entry.id === 'chat' 的分支——问答条目的连带死代码（计数徽标）又回来了")
	}
}
