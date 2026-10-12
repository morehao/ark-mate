package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住一条结构性约束（2026-10-11，V21）：**开发态与交付态只有一条资产路径**。
//
// 背景：在这之前，人判断"界面有没有问题"时看的是 `make dev` 起的 vite dev server（`:5173`），
// 它从**源码现编译**；而交付的是 `apps/web/dist`。两者不是同一次构建——CSP 三条限制不会被下发、
// `build-id.txt` 不存在（版本闸门只能判"不可判定"）、antd 会多打 `css-dev-only-*` 类名、
// React 的开发期警告还在。于是"**已被验证过的功能，从未在交付件形态上跑过**"（2026-10-11 实测：
// 用户已验功能的 web 端就是 `:5173`）。这个缺口不会让任何别的测试变红——它只会让人在错误的
// 资产上下结论，所以必须有一条机器判据。
//
// 判据分四段，全部 **fail-closed**（读不到、解析不到即失败，而不是静默变绿）：
//   ① 源码里不得再有编译期开发分支（`import.meta.env`）；
//   ② `apps/web/vite.config.ts` 必须在 `serve` 上直接报错——**让第二条路起不来**，而不只是"不推荐"；
//   ③ `scripts/dev-loop.mjs` 必须是"构建 → watch 重建 → 真实宿主服务产物"的形状；
//   ④ `make dev` 必须走 ③，且不得自己起 dev server（含重新引入跨源登记的 `MATE_ALLOWED_ORIGINS`）。
//
// **fail-loud 那半不在这里**：脚本"阶段自死即回红"是行为，用动态验收钉（kill 掉 watcher 阶段，
// 看退出码是否非 0）比读源码可靠。这里只钉住 `child.on('exit'` 这个监督机制确实在——
// 换了监督机制的实现，请同步这条判据（守卫的价值就在于"换实现要显式改守卫"）。

// devEnvPattern 命中任何读 `import.meta.env` 的代码（含 `.DEV` / `.PROD` / `.MODE`）。
//
// 为什么不只盯 `.DEV`：这一维度上任何一个成员都在表达"这段代码只在某一种构建里生效"，
// 那正是被取消掉的分叉；盯单个成员会漏掉换个写法的同一个问题。
var devEnvPattern = regexp.MustCompile(`import\.meta\.env`)

// TestDevLoopServesBuiltArtifact 断言开发循环服务的是构建产物，且不存在第二条资产路径。
func TestDevLoopServesBuiltArtifact(t *testing.T) {
	root := frontendRoot(t)

	t.Run("① apps/web 源码不得再有编译期开发分支", func(t *testing.T) {
		files := listUISources(t) // 目录不存在 / 零源码 → fatal（fail-closed）；界面可能在 packages/console
		var hits []violator
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("读 %s 失败: %v", f, err)
			}
			// 剥注释：解释"为什么不要写 DEV 分支"的注释本身不该让守卫变红。
			for i, line := range strings.Split(stripTSComments(string(raw)), "\n") {
				if devEnvPattern.MatchString(line) {
					rel, _ := filepath.Rel(filepath.Dir(root), f)
					hits = append(hits, violator{Rel: rel, Line: i + 1, Text: line})
				}
			}
		}
		reportViolations(t, "界面源码里又出现了 import.meta.env 分支——开发态与交付态因此重新分叉", hits)
	})

	t.Run("② vite dev server 必须起不来", func(t *testing.T) {
		cfg := filepath.Join(root, "apps", "web", "vite.config.ts")
		raw, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatalf("读 %s 失败: %v（配置被改名/移动了？这条守卫不能静默失效）", cfg, err)
		}
		code := stripTSComments(string(raw))
		if !strings.Contains(code, `env.command === 'serve'`) {
			t.Error("apps/web/vite.config.ts 不再在 `serve` 上判死——vite dev server 又能起来了，于是" +
				"「人验的资产」与「交付的资产」可以再次不是同一次构建（这正是 2026-10-11 修掉的那条分叉）")
		}
		if !strings.Contains(code, "throw") {
			t.Error("apps/web/vite.config.ts 里 `serve` 分支没有抛错，只是算了算——那等于没拦")
		}
	})

	t.Run("③ dev-loop 是「构建 → watch → 真实宿主服务产物」的形状", func(t *testing.T) {
		script := filepath.Join(root, "scripts", "dev-loop.mjs")
		raw, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("读 %s 失败: %v（开发循环搬走了？这条守卫不能静默失效）", script, err)
		}
		code := stripTSComments(string(raw))
		for _, marker := range []struct{ Text, Why string }{
			{"tsc", "类型闸门阶段"},
			{"vite", "资产 watcher 阶段"},
			{"--watch", "两个阶段都必须是常驻 watcher"},
			{"--no-emptyOutDir", "watch 重建不得清空核心正在服务的产物树"},
			{"--web-dir", "核心必须被指向**构建产物**（否则又回到从源码服务）"},
			{"child.on('exit'", "阶段监督（fail-loud）：阶段自死必须能拖垮整个循环"},
		} {
			if !strings.Contains(code, marker.Text) {
				t.Errorf("scripts/dev-loop.mjs 里找不到 %q（%s）——开发循环的形状变了，"+
					"请确认「服务唯一构建产物 + 阶段自死即回红」这两条仍在，并同步本守卫", marker.Text, marker.Why)
			}
		}
	})

	t.Run("④ make dev 走 dev-loop，且自己不起 dev server", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(root, "..", "Makefile"))
		if err != nil {
			t.Fatalf("读 Makefile 失败: %v", err)
		}
		recipe := devTargetRecipe(t, string(raw))
		if !strings.Contains(recipe, "dev-loop.mjs") {
			t.Error("`make dev` 不再走 scripts/dev-loop.mjs")
		}
		for _, banned := range []struct{ Text, Why string }{
			{"pnpm dev", "直接起 vite dev server = 从源码服务，资产路径重新分叉"},
			{"vite", "dev 目标的 recipe 里不该出现 vite（watch 由 dev-loop.mjs 起）"},
			{"MATE_ALLOWED_ORIGINS", "服务产物时页面与接口同源；重新登记跨源意味着又把 dev server 拉了回来"},
		} {
			if strings.Contains(recipe, banned.Text) {
				t.Errorf("`make dev` 的 recipe 里出现了 %q：%s", banned.Text, banned.Why)
			}
		}
	})

	t.Run("机制自检", func(t *testing.T) {
		// 剥注释：注释里的 import.meta.env 不该命中，代码里的必须命中，且行号按原文计。
		sample := "const a = 1 // import.meta.env.DEV\nif (import.meta.env.DEV) return\n"
		stripped := stripTSComments(sample)
		lines := strings.Split(stripped, "\n")
		if devEnvPattern.MatchString(lines[0]) {
			t.Error("剥注释失效：行注释里的 import.meta.env 被判成命中，守卫会变噪声")
		}
		if !devEnvPattern.MatchString(lines[1]) {
			t.Error("剥注释把真正的代码也吃掉了：守卫会全绿（这比红更糟）")
		}

		// recipe 提取：必须只取 `dev:` 目标，不能被 `dev/runtime-bin:` 或后续目标串进来。
		fake := "# dev: 注释\n" +
			"dev: dep\n" +
			"\t@echo hi\n" +
			"\tnode scripts/dev-loop.mjs\n" +
			"\n" +
			"dev/runtime-bin:\n" +
			"\t@echo 不该被取到\n" +
			"other:\n" +
			"\tpnpm dev\n"
		got := devTargetRecipe(t, fake)
		if !strings.Contains(got, "dev-loop.mjs") || strings.Contains(got, "不该被取到") || strings.Contains(got, "pnpm dev") {
			t.Errorf("devTargetRecipe 提取口径错了（会把别的目标的 recipe 或注释算进来）：%q", got)
		}
	})
}

// devTargetRecipe 返回 Makefile 里 `dev:` 目标的 recipe 行。
//
// 只认行首精确的 `dev:`：同名的 `dev/runtime-bin:`、`desktop/dev:` 都必须被排除——
// 把它们的 recipe 算进来会让"dev 目标里没有 vite"这条判据假绿。
func devTargetRecipe(t *testing.T, makefile string) string {
	t.Helper()
	return targetRecipe(t, makefile, "dev:", "`dev:` 目标")
}

// runtimeBinRecipe 返回 `dev/runtime-bin:` 目标的 recipe 行（生成转发 shim 的那一段）。
//
// 2026-10-12（T7）加：这段 recipe 里 shim 的**名字**必须是 `$(RUNTIME)` 派生——写死成某个具体
// runtime 时，构建期固定为另一个的构建上这个覆盖就成了没人调用的死文件（静默 no-op）。
func runtimeBinRecipe(t *testing.T, makefile string) string {
	t.Helper()
	return targetRecipe(t, makefile, "dev/runtime-bin:", "`dev/runtime-bin:` 目标")
}

// targetRecipe 取某个目标的 recipe 行（制表符开头），拼成一段文本。
func targetRecipe(t *testing.T, makefile, target, label string) string {
	t.Helper()
	var (
		out       []string
		inTarget  bool
		sawTarget bool
	)
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, target):
			inTarget, sawTarget = true, true
			continue
		case inTarget && strings.HasPrefix(line, "\t"):
			out = append(out, line)
			continue
		case inTarget && strings.TrimSpace(line) == "":
			continue // recipe 之间的空行：继续往下看（也可能已经走出该目标）
		case inTarget:
			inTarget = false // 撞到下一个目标或普通注释行
		}
	}
	if !sawTarget {
		t.Fatalf("Makefile 里找不到 %s——它被改名/删除了？这条守卫不能静默失效", label)
	}
	if len(out) == 0 {
		t.Fatalf("%s 看起来没有 recipe（判据失去对象）", label)
	}
	return strings.Join(out, "\n")
}

// targetHeader 返回某个目标的**声明行**（含 `\` 续行），例如 `dev: web-assets dev/runtime-bin`。
//
// recipe 行里看不到"这个目标依赖了谁"，而依赖丢了是**静默**的：PATH 前置目录照样在，只是里面没有
// 那份 shim，于是核心回落去 PATH 上找——又变回"跑本机那份"。
func targetHeader(t *testing.T, makefile, target, label string) string {
	t.Helper()
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, target) {
			continue
		}
		var out []string
		for j := i; j < len(lines); j++ {
			out = append(out, lines[j])
			if !strings.HasSuffix(strings.TrimSpace(lines[j]), "\\") {
				break
			}
		}
		return strings.Join(out, " ")
	}
	t.Fatalf("Makefile 里找不到 %s 的目标声明行——它被改名/删除了？这条守卫不能静默失效", label)
	return ""
}

// TestDevLoopReportsRuntimeHit 钉住第二条边界（2026-10-11，P4/D6）：**dev 态必须说清它跑的是哪一份底座**。
//
// 背景：dev 的核心按**写死的名字**（`opencode` / `dsh`）经 `exec` 走 PATH 解析，默认命中的是本机装的
// 那份；交付态（桌面壳）用的是随包载荷。两者可以不同代（2026-10-10 实测：本机 1.18.35 / 随包 2.0.26），
// 而这件事此前**完全静默**——`make dev` 的回显里连运行时名字都写死成 `opencode`，可构建期固定的
// runtime 早就是 `dsh` 了（于是那行回显还在替一个不会被调用的覆盖做宣传）。
//
// 四段判据，全部 fail-closed（读不到 / 解析不到即失败）：
//
//	① `scripts/dev-loop.mjs` 必须把命中事实**交给核心**（`--payloads`）——否则 `/meta.payloads[]` 在 dev 态
//	   恒为空，"这次跑的是哪份"又只剩一句可能过期的横幅；同时它必须带**来源判定的两个输入**
//	   （`MATE_DEV_RUNTIME_BIN` / `MATE_DEV_PAYLOADS_ROOT`），否则判定只能靠猜。
//	② `make dev` 的 recipe 必须把这两个输入交给循环，且**不得再出现写死运行时名字的那行回显**。
//	③ 验收里的比较必须是**报告**而不是判据：`web-ui-acceptance.mjs` 的 `reportRuntimeHit` 函数体里
//	   不得出现 `check(` / `fail(` / `results.push(`——D6 的口径是"不一致时打印两者并 WARN，不判红"。
//	   这条不写成"注释里说别判红"，因为那种约定没人执行；函数体是能被解析的。
//	④ **覆盖口必须真的生效**（2026-10-12 加，T7）：转发 shim 的名字必须与核心被编译成调用的名字
//	   **同源**（都用 `$(RUNTIME)`），且 `make dev` / `make server` 都得依赖那个生成目标。
//	   ④ 挡的是**静默 no-op**：此前 shim 名写死 `opencode`，于是构建期固定 `dsh` 时
//	   `OPENCODE_BIN=…` 只是让 PATH 上多了个没人调用的文件——用户以为换了底座，核心照旧拉本机那份。
//	   当时的处置是"回显里如实说不会生效"（横幅 + `inert` 分流）；根因修掉后那套绕道整体删除，
//	   于是这条判据只能钉根因——否则同一个坑会以"没人看横幅"的形式复发。
func TestDevLoopReportsRuntimeHit(t *testing.T) {
	root := frontendRoot(t)

	loop := readFrontendFile(t, filepath.Join(root, "scripts", "dev-loop.mjs"))
	strippedLoop := stripTSComments(loop)
	for _, want := range []struct{ Text, Why string }{
		{"'--payloads'", "命中事实没有交给核心：`/meta.payloads[]` 在 dev 态会退回恒空，D3 的可见性缺口复发"},
		{"MATE_DEV_RUNTIME_BIN", "循环没有拿到显式覆盖那份（判定输入缺一半）"},
		{"MATE_DEV_RUNTIME_BIN_SOURCE", "循环不知道覆盖的来源（`命令行`/`环境变量`/`.env` 就报不出来）"},
		{"MATE_DEV_PAYLOADS_ROOT", "循环不知道随包载荷在哪：版本只能靠猜"},
	} {
		if !strings.Contains(strippedLoop, want.Text) {
			t.Errorf("dev-loop.mjs 里找不到 %q：%s", want.Text, want.Why)
		}
	}
	// `--payloads` 必须真的进了核心 argv（而不是只写在注释/常量里）。探到 `resolveRuntimeHit()` 的
	// 调用点是这张网的最小形状：删掉调用，横幅与 `--payloads` 会**同时**静默消失。
	if !strings.Contains(strippedLoop, "resolveRuntimeHit(") || !strings.Contains(strippedLoop, "reportRuntimeHit(") {
		t.Error("dev-loop.mjs 没有调用命中判定/报告——横幅与 `--payloads` 会一起消失，而别的测试都不会红")
	}

	makefile := readFrontendFile(t, filepath.Join(root, "..", "Makefile"))
	recipe := devTargetRecipe(t, makefile)
	for _, want := range []struct{ Text, Why string }{
		{"MATE_DEV_RUNTIME_BIN=", "`make dev` 没把显式覆盖交给循环：判定输入缺一半"},
		{"MATE_DEV_PAYLOADS_ROOT=", "`make dev` 没把随包载荷根交给循环"},
	} {
		if !strings.Contains(recipe, want.Text) {
			t.Errorf("`make dev` 的 recipe 里找不到 %q：%s", want.Text, want.Why)
		}
	}
	if strings.Contains(recipe, "底座  opencode") {
		t.Error("`make dev` 的 recipe 里又出现了写死「opencode」的底座回显——" +
			"它替一个不会被调用的覆盖做宣传（构建期固定的 runtime 默认是 dsh），这正是 D3 的静默缺口")
	}

	// ④ 覆盖口必须真的生效：shim 名与核心调用的名字同源（都用 `$(RUNTIME)`），且 dev/server 都依赖生成目标。
	bin := runtimeBinRecipe(t, makefile)
	if !strings.Contains(bin, "$(DEV_BIN_DIR)/$(RUNTIME)") {
		t.Error("`dev/runtime-bin` 没有按 `$(RUNTIME)` 命名转发 shim：构建期固定为别的底座时，" +
			"`OPENCODE_BIN=…` 会退回**静默 no-op**（PATH 上多了个没人调用的文件，核心照旧拉本机那份）")
	}
	// 「同源」的另一半：核心必须是被编译成调用 `$(RUNTIME)` 的。少了这条，上面那句会假绿——
	// 把 BuildRuntime 写死成 dsh 时 shim 名跟着 RUNTIME 变，可核心永远只找 dsh。
	if !strings.Contains(makefile, "BuildRuntime=$(RUNTIME)") {
		t.Error("Makefile 不再把 `$(RUNTIME)` 注入核心的 BuildRuntime：shim 名与核心实际调用的名字" +
			"失去同源关系，「shim 按 $(RUNTIME) 命名」这条判据会假绿")
	}
	for _, dep := range []struct{ Target, Label string }{
		{"dev:", "`dev:`"}, {"server:", "`server:`"},
	} {
		if !strings.Contains(targetHeader(t, makefile, dep.Target, dep.Label), "dev/runtime-bin") {
			t.Errorf("%s 不再依赖 `dev/runtime-bin`：转发 shim 不会被生成，而 PATH 前置目录**照样在**——"+
				"核心会静默回落到 PATH 上本机那份（正是这个覆盖口要消灭的形态）", dep.Label)
		}
	}
	if strings.Contains(strippedLoop, "RUNTIME === 'opencode'") {
		t.Error("dev-loop.mjs 里又出现了「按构建期 runtime 决定覆盖是否生效」的分支" +
			"（`RUNTIME === 'opencode'`）——那等于承认 shim 名又写死了，覆盖重新变回静默 no-op")
	}

	acc := stripTSComments(readFrontendFile(t, filepath.Join(root, "scripts", "web-ui-acceptance.mjs")))
	body := functionBody(t, acc, "reportRuntimeHit")
	for _, banned := range []string{"check(", "fail(", "results.push("} {
		if strings.Contains(body, banned) {
			t.Errorf("验收的命中报告里出现了 %q：D6 定的是「打印两者 + WARN，**不判红**」——"+
				"把 dev 跑本机底座变成红，只会逼人关掉 `make dev` 与这份报告", banned)
		}
	}
	if !strings.Contains(body, "无") || !strings.Contains(body, "payloads") {
		t.Error("命中报告读起来不像「读 /meta.payloads[] 并打印两者」——判据要跟着实现走，别让它空转")
	}
}

// functionBody 返回 TS/JS 源码里 `function <name>(` 的函数体（大括号配对）。
//
// 找不到函数名即 fatal：改名会让"函数体里不许判红"这条判据**静默失效**（那比红更糟）。
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	head := "function " + name + "("
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("在 %s 里找不到 %q——改名/搬迁会让这条守卫静默空转", "web-ui-acceptance.mjs", head)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("%s 的签名里没有 `{`（解析不到函数体）", name)
	}
	start := i + open
	depth := 0
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				body := src[start : j+1]
				if len(body) < 40 {
					t.Fatalf("%s 的函数体只解析出 %d 字节——大括号配对看起来失效了", name, len(body))
				}
				return body
			}
		}
	}
	t.Fatalf("%s 的函数体没有闭合的大括号", name)
	return ""
}

// readFrontendFile 读一个前端/仓库文件，失败即 fatal（守卫的对象不见了不能静默变绿）。
func readFrontendFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return string(raw)
}
