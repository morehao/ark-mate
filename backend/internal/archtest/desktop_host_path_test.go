package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是**桌面「不依赖宿主机环境」**的静态判据（2026-10-10，第 21 条）。
//
// 为什么值得一条静态守卫：桌面分发的承诺是"机器上不需要 Go / Node / pnpm，也没装过 dsh / opencode"。
// 这件事有**两半证据**：
//
//  1. 动态（验收脚本）：诱饵目录 + 最小 `PATH` + 真 dsh 会话，证明**运行期没有咨询宿主**
//     （`frontend/scripts/desktop-no-host-acceptance.mjs`）；
//  2. 静态（本守卫）：进包并在运行期生效的代码里**根本不存在**宿主工具的绑定——哪怕诱饵恰好没覆盖到
//     （例如某个写了绝对路径的分支今天没被走到），也不该有这条路。
//
// 动态那半只在"跑到的那条路径"上成立；静态这半管的是"**有没有**这条路"。两者缺一不可。
//
// 与 `desktop_payload_test.go`（第 18 条）的分工：那条禁的是**探测写法**（`which` / `command -v` /
// `LookPath`——"去看看机器上有没有"），本守卫禁的是**硬引用**（`/opt/homebrew/bin/node` 这类
// "直接把宿主那一份写死"）。一个禁"问"，一个禁"绑"。
//
// 扫描面（**刻意只扫"进包并在运行期生效"的部分**，理由逐条给）：
//
//   - `frontend/apps/desktop/src/**`：壳的主进程/preload/渲染进程代码，**整棵目录**扫（不是白名单列表）
//     ——新增一个 `host-detect.ts` 会立刻被扫到，不会因为"忘了登记"而漏。
//   - `frontend/apps/desktop/scripts/gen-runtime-bin.mjs`：它**生成**随包私有启动器
//     （`Resources/runtime/bin/{node,npx,dsh,opencode}`），生成物在包里被 shell 执行。宿主绑定若从这里
//     漏进去，症状是"开发机上一切正常、干净机器上 exec 空命令"。
//
// **不扫**构建期脚本（`prepare-runtime.mjs` / `build.mjs` / `desktop-*.mjs` 验收脚本 / `Makefile`）：
// 它们跑在开发者机器上，依赖宿主 node 是**正常的**（`make desktop/dist` 本来就要求宿主有 Node）。
// 把构建期一起扫会把守卫变成噪声，噪声守卫等于没有守卫。
//
// 与 `desktop_payload_test.go` 同一范式（Go 读 TS/JS 源码、不向前端引任何测试框架），三条纪律：
//  1. **fail-closed**：扫描对象不存在（被改名/删除/还没落地）判失败，绝不"扫不到就变绿"；
//  2. 先**剥注释**再扫：这些文件的注释是中文、会解释"不要写死宿主路径"，正文里出现该词不该红；
//  3. 机制**自检**：剥注释与命中口径用内存样本自证（写错会全绿或全红）。

// desktopRuntimeSrcDir 是壳侧源码目录（相对仓库根），整棵扫。
const desktopRuntimeSrcDir = "frontend/apps/desktop/src"

// desktopHostPathFiles 是除壳源码外、**逐文件**扫描的对象（相对仓库根）。
//
// 为什么它是列表而不是整棵目录：`apps/desktop/scripts/` 下**混着**构建期脚本（不该扫），
// 只有"生成随包内容"的这一支要扫。列表短，且有 fail-closed 兜底。
var desktopHostPathFiles = []string{
	// 生成随包私有启动器（`Resources/runtime/bin/*`）——生成物进包并被 shell 执行。
	"frontend/apps/desktop/scripts/gen-runtime-bin.mjs",
}

// hostBindingPattern 一条禁止出现的**宿主绑定**写法。
type hostBindingPattern struct {
	Pattern *regexp.Regexp
	Why     string
}

// desktopHostBindingPatterns 禁止在运行期代码里出现的宿主绑定。
//
// 判据不是"看起来像不像"，而是**包外与否**：包内允许（`$MATE_NODE_EXECUTABLE`、`process.execPath`、
// 生成器写的 `#!/bin/sh` —— 系统基线），包外一律禁止。命中清单要求**今天零命中**（先跑一遍确认不误报
// 再定稿）：`gen-runtime-bin.mjs:109-125` 现在用的正是 `#!/bin/sh` + `exec "$MATE_NODE_EXECUTABLE"`。
//
// 顺序有意义：同一行只报第一条（失败信息不刷屏），所以更具体的 shebang 形态排在通用 `env x` 之前。
var desktopHostBindingPatterns = []hostBindingPattern{
	// shebang 形态：`#!/usr/bin/env node` —— 把解释器交给宿主 PATH 上的 node 决定。
	{regexp.MustCompile(`/usr/bin/env\s+(node|npx|go|pnpm|dsh|opencode|bun)\b`),
		"不得把宿主解释器写进 shebang（`#!/usr/bin/env node` 形态）——包内启动器必须 `#!/bin/sh` + 显式变量"},
	// 通用命令形态：`env node`、`env -i KEY=VALUE node`（覆盖 non-shebang 拼接场景；允许中间夹选项与赋值）。
	{regexp.MustCompile(`\benv\s+(?:(?:-\S+|\S+=\S*)\s+)*(node|npx|go|pnpm|dsh|opencode|bun)\b`),
		"不得用 `env <工具>` 的形态绑定宿主解释器/底座"},
	// Homebrew（Apple Silicon）。
	{regexp.MustCompile(`/opt/homebrew/`),
		"不得引用 `/opt/homebrew/` 下的宿主工具（Apple Silicon Homebrew）"},
	// Homebrew（Intel）/ 手动安装 / make install 落点。
	{regexp.MustCompile(`/usr/local/bin/`),
		"不得引用 `/usr/local/bin/` 下的宿主工具（Intel Homebrew、手动安装）"},
	// 系统目录里恰好存在的同名工具（Linux 常见：`/usr/bin/node`）。
	{regexp.MustCompile(`/usr/bin/(node|npx|go|pnpm|dsh|opencode|bun)\b`),
		"不得引用 `/usr/bin/` 下的宿主工具（发行版自带的 node/bun 等）"},
	// 版本管理器目录：nvm / volta / asdf / fnm（本仓 `frontend/.nvmrc` 说明团队用 fnm）。
	{regexp.MustCompile(`\.(nvm|volta|asdf|fnm)/`),
		"不得引用版本管理器目录（`.nvm/`、`.volta/`、`.asdf/`、`.fnm/`）里的宿主 Node"},
}

// TestDesktopRuntimeMustNotBindHostToolPaths 桌面运行期代码不得绑定宿主工具路径（2026-10-10，第 21 条）。
//
// 失败信息给到 文件 + 行号 + 命中片段，便于直接跳过去改。
func TestDesktopRuntimeMustNotBindHostToolPaths(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")

	// ① fail-closed：扫描对象必须都在。
	srcDir := filepath.Join(repoRoot, desktopRuntimeSrcDir)
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("桌面壳源码目录不存在：%s（%v）——守卫没有可检查的对象（fail-closed：不因改名/删除静默通过）\n"+
			"若壳源码换了目录，请同步 desktopRuntimeSrcDir", desktopRuntimeSrcDir, err)
	}
	var missing []string
	for _, rel := range desktopHostPathFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			missing = append(missing, fmt.Sprintf("  %s（%v）", rel, err))
		}
	}
	if len(missing) > 0 {
		t.Fatalf("桌面运行期源文件不存在，守卫没有可检查的对象（fail-closed）：\n%s\n"+
			"若生成器换了名字/位置，请同步 desktopHostPathFiles", strings.Join(missing, "\n"))
	}

	// ② 收集扫描对象：壳源码**整棵目录**（相对仓库根），加上逐文件名单。
	scan := listFrontendSources(t, srcDir)
	for _, rel := range desktopHostPathFiles {
		scan = append(scan, filepath.Join(repoRoot, rel))
	}

	// ③ 逐文件剥注释后逐行扫描。
	var violations []violator
	for _, abs := range scan {
		raw, err := os.ReadFile(abs)
		if err != nil {
			t.Errorf("读 %s 失败: %v", abs, err)
			continue
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil {
			rel = abs
		}
		violations = append(violations, scanHostBindingLines(filepath.ToSlash(rel), string(raw))...)
	}
	if len(violations) > 0 {
		lines := make([]string, 0, len(violations))
		for _, v := range violations {
			lines = append(lines, "  "+v.String())
		}
		t.Errorf("桌面运行期代码出现宿主工具绑定（%d 处）：\n%s\n"+
			"桌面分发的承诺是「机器上没有 Node/pnpm，也没装过 dsh/opencode 也能跑」：\n"+
			"  · 要 Node → 用壳自己那份（`MATE_NODE_EXECUTABLE` / `process.execPath`），不要找宿主 PATH；\n"+
			"  · 要底座 → 只认随包载荷（`payload.ts` 的显式解析链），不要引宿主安装目录；\n"+
			"  · 生成启动器 → `#!/bin/sh` + 显式变量（`gen-runtime-bin.mjs` 现在就是这么写的）。\n"+
			"构建期脚本（跑在开发者机器上）不在这条守卫的范围内。",
			len(violations), strings.Join(lines, "\n"))
	}

	// ④ 机制自检：这条守卫的价值全在"剥注释 + 首行 shebang 例外 + 命中口径"这几个机械动作上。
	// 用内存样本自证，不依赖被扫文件的内容（子测试不占顶层守卫条数）。
	t.Run("剥注释与命中口径自检", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want []int // 期望命中的行号
		}{
			{"Homebrew 路径命中", "const node = '/opt/homebrew/bin/node'\n", []int{1}},
			{"/usr/local/bin 命中", "exec('/usr/local/bin/dsh', [])\n", []int{1}},
			{"nvm 目录命中", "const p = `${home}/.nvm/versions/node/v24/bin/node`\n", []int{1}},
			{"fnm 目录命中", "const p = `${home}/.fnm/node-versions/v24/installation/bin/node`\n", []int{1}},
			{"shebang 形态命中（模板串里）", "const shebang = '#!/usr/bin/env node'\n", []int{1}},
			{"env node 命中", "execSync('env node app.js')\n", []int{1}},
			{"env -i 夹选项也命中", "run('env -i PATH=/usr/bin node app.js')\n", []int{1}},
			{"/usr/bin/node 命中", "spawn('/usr/bin/node', ['x.js'])\n", []int{1}},
			{"行注释里的路径不算", "// 不要写 /opt/homebrew/bin/node\nconst a = 1\n", nil},
			{"块注释里的路径不算", "/* /usr/local/bin/dsh 也别写 */\nconst a = 1\n", nil},
			{"文件首行 shebang 例外（脚本自身入口）", "#!/usr/bin/env node\nconst a = 1\n", nil},
			{"非首行的 shebang 仍命中", "const a = 1\n#!/usr/bin/env node\n", []int{2}},
			{"包内写法不命中", "exec('\"$MATE_NODE_EXECUTABLE\" app.js')\nconst sh = '#!/bin/sh'\n", nil},
			{"process.execPath 不命中", "spawn(process.execPath, ['app.js'])\n", nil},
			{"URL 里的 // 不吞掉后面的代码", "const u = 'https://example.test/x'\nspawn('/usr/bin/node')\n", []int{2}},
			{"命中多行", "'/opt/homebrew/bin/node'\n// .nvm/x\n'/usr/local/bin/dsh'\n", []int{1, 3}},
		}
		for _, tc := range cases {
			got := scanHostBindingLines("sample.ts", tc.src)
			var lines []int
			for _, v := range got {
				lines = append(lines, v.Line)
			}
			if len(lines) != len(tc.want) {
				t.Errorf("%s: 命中行号 got %v want %v（%v）", tc.name, lines, tc.want, got)
				continue
			}
			for i := range tc.want {
				if lines[i] != tc.want[i] {
					t.Errorf("%s: 命中行号 got %v want %v", tc.name, lines, tc.want)
					break
				}
			}
		}
	})
}

// scanHostBindingLines 扫描一段 TS/JS 源码（**先剥注释**），返回逐行的宿主绑定违规（行号按原文计）。
//
// 首行 shebang 例外：脚本**自身**的第一行 `#!/usr/bin/env node` 是开发者机器上的入口约定
// （`node scripts/xxx.mjs` 之外的直接执行方式），它不会作为**内容**进包——进包的是生成器写出去的那份
// 启动器，那部分在模板串里，照扫不误（自检用例里"shebang 形态命中（模板串里）"钉的就是这条）。
func scanHostBindingLines(rel, src string) []violator {
	var out []violator
	for i, line := range strings.Split(stripTSComments(src), "\n") {
		if i == 0 && strings.HasPrefix(strings.TrimSpace(line), "#!") {
			continue
		}
		for _, p := range desktopHostBindingPatterns {
			if hit := p.Pattern.FindString(line); hit != "" {
				out = append(out, violator{
					Rel:  rel,
					Line: i + 1,
					Text: fmt.Sprintf("%s ← %s（命中 %s）", strings.TrimSpace(line), p.Why, hit),
				})
				break // 同一行只报第一条，失败信息不刷屏
			}
		}
	}
	return out
}
