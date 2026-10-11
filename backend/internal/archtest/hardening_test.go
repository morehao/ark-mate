package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/router"
)

// 本文件是**桌面渲染进程加固基线**的守卫（M1-5，设计 §9.2）。
//
// 用 Go 读 TS 源码的两条理由：
//  1. CSP 是**两处下发同一策略**（核心响应头 + 壳响应头），前端契约是唯一真源——
//     只在 TS 里写一遍、Go 里抄一遍，两边漂移不会有任何编译错误；
//  2. webPreferences 的加固项（contextIsolation / sandbox / nodeIntegration / webviewTag /
//     无 enableRemoteModule）是"少了不报错、多了才出事"的安全配置，静态断言比运行时断言更早。

// cspPolicyInContract 从 `@ark-mate/desktop-contract` 的 index.ts 里取出 CSP_POLICY 的字面量。
func cspPolicyInContract(t *testing.T) string {
	t.Helper()
	path := filepath.Join(frontendRoot(t), "packages", "desktop-contract", "src", "index.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v（契约包被改名/移动了？）", path, err)
	}
	src := string(raw)
	idx := strings.Index(src, "CSP_POLICY")
	if idx < 0 {
		t.Fatalf("%s 里找不到 CSP_POLICY（改名了？守卫已失效）", path)
	}
	// 允许 `= "…"` 与 `=\n  "…"` 两种写法（契约里为可读性换行过）。
	re := regexp.MustCompile(`(?s)CSP_POLICY\s*=\s*"([^"]*)"`)
	m := re.FindStringSubmatch(src[idx:])
	if m == nil {
		t.Fatalf("%s 里 CSP_POLICY 不是紧跟一个双引号字面量（解析失效）", path)
	}
	return m[1]
}

// TestAssetContentTypesMatchContract 静态资产的 Content-Type 映射两侧逐字一致（M2-5）。
//
// 同一个文件经壳（`net.fetch(file://)` + 映射表）与核心（Go mime 表 + 映射表）两条路径下发；
// 表不一致的表现是"图标在桌面窗口里正常、在浏览器入口里坏掉"这类只在一侧出现的怪现象——
// 而 asset-parity 只能在运行时抓到**已经出现**的分歧，静态守卫能在加扩展名的那一刻就红。
func TestAssetContentTypesMatchContract(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "packages", "desktop-contract", "src", "index.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	src := string(raw)
	block := src[strings.Index(src, "ASSET_CONTENT_TYPES"):]
	block = block[:strings.Index(block, "}")]
	re := regexp.MustCompile(`'([^']+)':\s*'([^']+)'`)
	fromContract := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		fromContract[m[1]] = m[2]
	}
	if len(fromContract) == 0 {
		t.Fatal("契约里的 ASSET_CONTENT_TYPES 解析为空（结构变了？守卫已失效）")
	}
	for ext, ct := range fromContract {
		if got, ok := router.MimeOverrides[ext]; !ok {
			t.Errorf("核心缺 %s 的映射（前端契约有 %q）", ext, ct)
		} else if got != ct {
			t.Errorf("%s 的映射不一致：前端 %q / 核心 %q", ext, ct, got)
		}
	}
	for ext, ct := range router.MimeOverrides {
		if _, ok := fromContract[ext]; !ok {
			t.Errorf("核心多出 %s 的映射（%q）：前端契约里没有，两条路径会分歧", ext, ct)
		}
	}
}

// TestBuildIDUnknownMatchesContract `unknown` 这个口径在核心与前端契约里必须逐字一致（M2-4）。
//
// 为什么值得一条守卫：版本闸门的判定是"两侧都是已知值才比"。如果核心报 `unknown`、
// 前端认的是 `Unknown`/`n/a`，闸门就会把"校验不了"当成"不一致"，把用户挡在整屏状态页外面——
// 一个拼写错误变成一次"应用打不开"。
func TestBuildIDUnknownMatchesContract(t *testing.T) {
	if router.BuildIDUnknown != "unknown" {
		t.Fatalf("核心口径变了：BuildIDUnknown=%q", router.BuildIDUnknown)
	}
	path := filepath.Join(frontendRoot(t), "packages", "desktop-contract", "src", "gate.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v（版本闸门的契约被改名/移动了？）", path, err)
	}
	src := string(raw)
	if !strings.Contains(src, "UNKNOWN_BUILD_ID = 'unknown'") && !strings.Contains(src, `UNKNOWN_BUILD_ID = "unknown"`) {
		t.Fatalf("前端契约的 UNKNOWN_BUILD_ID 不再等于 'unknown'：闸门会把\"校验不了\"误判成\"不一致\"")
	}
	if !strings.Contains(src, "export function versionGate(") {
		t.Fatal("前端契约缺少 versionGate 纯判定函数（M2-4）")
	}
}

// TestRendererHardeningBaseline 渲染进程加固基线（M1-5）：
//   - CSP 双源逐字一致（前端契约 ↔ 核心响应头常量）；
//   - webPreferences 的四项取值正确，且**不存在** enableRemoteModule（Electron 44 已移除该选项，
//     写回去不报错但也没作用——历史上是远程模块攻击面，留着就是隐患）。
func TestRendererHardeningBaseline(t *testing.T) {
	t.Run("CSP 双源逐字一致", func(t *testing.T) {
		want := cspPolicyInContract(t)
		if want == "" {
			t.Fatal("契约里的 CSP_POLICY 为空")
		}
		if router.ContentSecurityPolicy != want {
			t.Errorf("CSP 两处不一致：\n  契约（@ark-mate/desktop-contract/index.ts）= %q\n  核心（internal/router/csp.go）    = %q", want, router.ContentSecurityPolicy)
		}
		// 纪律条款单独再钉一次：外域的"漏写"有时看起来无害（比如为了字体加个 CDN），
		// 但 R3 的判据是"除模型 API 与用户触发的 MCP 拉取外零出站"。
		if strings.Contains(want, "unsafe-eval") {
			t.Error("CSP 不得放开 unsafe-eval（前端产物不得依赖 eval）")
		}
		// 逐个列出策略里的 URL，凡不是回环的一律拒绝（Go 的 RE2 没有负向前瞻，直接枚举比对）。
		for _, u := range regexp.MustCompile(`(?:https?|wss?)://[^\s;]*`).FindAllString(want, -1) {
			if !strings.Contains(u, "127.0.0.1") {
				t.Errorf("CSP 不得列任何外域，发现 %q", u)
			}
		}
	})

	t.Run("webPreferences 加固项", func(t *testing.T) {
		path := filepath.Join(frontendRoot(t), "apps", "desktop", "src", "main.ts")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", path, err)
		}
		src := string(raw)
		start := strings.Index(src, "webPreferences")
		if start < 0 {
			t.Fatalf("%s 里找不到 webPreferences（窗口构造被重构了？守卫已失效）", path)
		}
		// 取 webPreferences 之后的第一段配平大括号块。
		block, ok := balancedBlock(src[start:])
		if !ok {
			t.Fatalf("%s 的 webPreferences 块不配平（解析失效）", path)
		}

		for _, want := range []string{
			"contextIsolation: true",
			"sandbox: true",
			"nodeIntegration: false",
			"webviewTag: false",
		} {
			if !strings.Contains(block, want) {
				t.Errorf("webPreferences 缺少加固项 %q（设计 §9.2）", want)
			}
		}
		if strings.Contains(block, "enableRemoteModule") {
			t.Error("webPreferences 不得出现 enableRemoteModule（Electron 44 已移除该选项，写回去等于把历史攻击面的念头留在代码里）")
		}
	})
}

// balancedBlock 从 `{` 起返回配平的大括号块（含首尾），用于把配置对象切出来。
// 只做括号计数——TS 里这个对象是纯字面量，没有模板串/正则里的括号干扰。
func balancedBlock(s string) (string, bool) {
	open := strings.Index(s, "{")
	if open < 0 {
		return "", false
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[open : i+1], true
			}
		}
	}
	return "", false
}

// TestDirectoryPickerIsTwoStateHostResolved 目录选择器的**两态编排**（v14.5，对齐 dsh 的
// `directory-picker-auto`）：由**宿主事实**决定用系统面板还是应用内浏览，且两态互斥。
//
// 为什么专门守：这条编排坏掉是静默的，而且已经坏过**两次**：
//  1. V1 只做了应用内那半（`native.pickDirectory` 在整个 Web 控制台零调用点）——桌面态白丢系统面板；
//  2. V2 把判据放在**客户端**（"有没有 Electron 桥 `__MATE_DESKTOP__`"）——于是**本机浏览器**
//     被判成"没有面板"，而参照实现的 `dsh web` 恰恰在那种场景下用系统面板（面板是宿主进程抬的）。
//
// 所以这条守卫钉的是"判据在服务端、执行在宿主编译单元"这几个机械事实：
//
//	a. 解析必须是纯函数，唯一判据是服务端声明（禁 UA / Electron 桥 / 进程平台）；
//	b. 三个选目录入口都必须先问解析，且必须调 `pickDirectoryNative()`（漏一处 = 那一处退回单态）；
//	c. 原生那半必须**打宿主端点**（`pickDirectoryHost`），而不是某个客户端桥；
//	d. 判定与命令的宿主实现必须在 Go 里存在（`workspace_picker.go` 的牌价与三条判据）。
func TestDirectoryPickerIsTwoStateHostResolved(t *testing.T) {
	// 界面已搬进 packages/console（P5）——这里**必须**跟着搬，否则下面每个 readSrc 都会
	// "读不到文件就 fatal"，而这条守卫的对象（三个选目录入口）就换成了不存在的路径。
	web := filepath.Join(frontendRoot(t), "packages", "console", "src")

	readSrc := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(web, rel))
		if err != nil {
			t.Fatalf("读 %s 失败: %v（改名/移动了？守卫已失效）", rel, err)
		}
		return stripTSComments(string(raw))
	}

	// a. 解析：纯函数，唯一判据是服务端声明。
	picker := readSrc("directoryPicker.ts")
	if !regexp.MustCompile(`(?s)export function resolveDirectoryPicker\(facts: DirectoryPickerFacts\): DirectoryPickerKind \{\s*return facts\.nativeDialog \? 'native' : 'browse'`).MatchString(picker) {
		t.Error("resolveDirectoryPicker 必须是纯函数 `facts.nativeDialog ? 'native' : 'browse'`（解析不到即失败）")
	}
	for _, forbidden := range []string{"navigator.userAgent", "window.electron", "__MATE_DESKTOP__", "process.platform"} {
		if strings.Contains(picker, forbidden) {
			t.Errorf("directoryPicker.ts 不得出现 %q：形态判据只认服务端声明（客户端嗅探会把本机浏览器误判成没有面板）", forbidden)
		}
	}

	// c. 原生那半必须打**宿主端点**（服务端跑 osascript/zenity；不是某个客户端桥）。
	if !strings.Contains(picker, "pickDirectoryHost(") {
		t.Error("directoryPicker.ts 必须通过宿主端点 `pickDirectoryHost()` 抬面板（客户端桥那版已被证伪）")
	}
	// 声明的读取点在 `capabilityFacts.ts`（2026-10-11：同一份声明还被设置面读来判入口形态，
	// 两处各读一次会有两个真相）。这里钉两件事：事实来自 `native-dialog` 声明、且 picker 只消费事实。
	facts := readSrc("capabilityFacts.ts")
	if !strings.Contains(facts, "native-dialog") {
		t.Error("capabilityFacts.ts 必须从 `/capabilities` 的 `native-dialog` 读判定结果")
	}
	if !strings.Contains(picker, "declaredFacts()") {
		t.Error("directoryPicker.ts 必须消费 `capabilityFacts.ts` 读到的事实（自己再读一次声明就会有两个真相）")
	}

	// b. 三个入口都必须问解析，并调用原生那半。
	entrypoints := []string{
		filepath.Join("components", "WorkspacePicker.tsx"),
		filepath.Join("components", "AddWorkspaceButton.tsx"),
		filepath.Join("components", "settings", "GeneralSettingsPanel.tsx"),
	}
	for _, rel := range entrypoints {
		src := readSrc(rel)
		if !strings.Contains(src, "directoryPickerKind() === 'native'") {
			t.Errorf("%s 没有先问 directoryPickerKind()：这个入口在宿主能抬面板时不会用它", rel)
		}
		if !strings.Contains(src, "pickDirectoryNative(") {
			t.Errorf("%s 的 native 分支没有调用 pickDirectoryNative(", rel)
		}
		// 取消必须是**非错误**路径：把它当失败报出来，用户每按一次 Esc 都会看到一个报错。
		if !strings.Contains(src, "'cancelled' in picked") {
			t.Errorf("%s 没有处理取消（`'cancelled' in picked`）：取消不是错误", rel)
		}
		for _, forbidden := range []string{"__MATE_DESKTOP__", "isDesktopCarrier()"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s 不得用 %q 判形态：判据在服务端（宿主事实），客户端形态不能决定", rel, forbidden)
			}
		}
	}

	// d. 宿主实现必须在 Go 里存在，且三条判据逐条可查（对齐 dsh 的 resolveDirectoryPickerBackend）。
	hostPath := filepath.Join(backendRoot(t), "internal", "manager", "workspace_picker.go")
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("读 %s 失败: %v（宿主实现被删/改名了？守卫失效）", hostPath, err)
	}
	host := stripGoComments(string(raw))
	for _, want := range []string{
		"func nativePickerMode(env pickEnv) NativePickerMode", // 判定是纯函数（注入宿主事实）
		"env.ssh",                    // ① 非 SSH
		"loopbackBind(env.bindAddr)", // ② 绑回环
		`"osascript"`,                // ③ 命令配方：macOS
		`"zenity"`,                   //    命令配方：Linux
		"ErrNativePickUnsupported",   //  无可用选择器必须是可判定错误
	} {
		if !strings.Contains(host, want) {
			t.Errorf("workspace_picker.go 缺少 %q（宿主判定/配方被改动了？）", want)
		}
	}
	if strings.Contains(host, "exec.Command(") == false && strings.Contains(host, "exec.CommandContext(") == false {
		t.Error("workspace_picker.go 最终必须真的 spawn 宿主命令（否则原生那半是空的）")
	}
}

// stripGoComments 去掉 Go 源码的行注释与块注释（保持字节可忽略：只用于 contains 断言）。
func stripGoComments(src string) string {
	out := []byte(src)
	const (
		code = iota
		lineComment
		blockComment
	)
	state := code
	for i := 0; i < len(out); i++ {
		switch state {
		case code:
			if out[i] == '/' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = lineComment
			} else if out[i] == '/' && i+1 < len(out) && out[i+1] == '*' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = blockComment
			}
		case lineComment:
			if out[i] == '\n' {
				state = code
			} else {
				out[i] = ' '
			}
		case blockComment:
			if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}
