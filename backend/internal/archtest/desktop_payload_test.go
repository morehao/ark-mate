package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是**桌面载荷解析**的守卫（M3-4，第 18 条）。
//
// 为什么值得一条静态守卫：桌面态的运行时解析链是**显式**的（设计 §6.5：
// 显式配置 → 随包 payload → PATH，命中级别如实上报给核心，最终出现在 `/meta` 的 payloads[]）。
// 一旦有人在壳里写 `which dsh` / `command -v dsh` / `LookPath('dsh')` 去"看看机器上有没有"，
// 后果不是报错而是**静默换人**：机器上恰好装过 dsh 时，"随包那份"会被用户自己装的那份顶掉，
// 版本、摘要、行为全变，而诊断面还以为命中的是随包载荷。这正是设计明令禁止的探测。
//
// 与 frontend_boundary_test.go 同一范式：Go 读 TS 源码、不向前端引任何测试框架。
// 两条纪律：
//  1. **fail-closed**——文件不存在（被改名/删除/还没落地）判为失败，绝不因为"扫不到东西"静默变绿；
//  2. 先**剥注释**再扫描——这两个文件的注释是中文、会解释"不要用 which 去探测"，
//     正文里出现该词不该红（自证时踩到过）。

// desktopPayloadFiles 是本守卫的扫描对象（相对仓库根）。
var desktopPayloadFiles = []string{
	"frontend/apps/desktop/src/payload.ts",
	"frontend/apps/desktop/src/node-runtime.ts",
}

// forbiddenProbe 一条禁止出现的探测写法。
type forbiddenProbe struct {
	// Pattern 命中的正则。
	Pattern *regexp.Regexp
	// Why 命中后给出的解释（失败信息要让人知道为什么不能这么写）。
	Why string
}

// desktopProbePatterns 禁止在载荷解析里出现的模式。
//
// 前两条覆盖 shell 式探测（`which x`、`command -v x`，也覆盖模板串里拼出来的 `which ${name}`），
// 第三条覆盖进程内 PATH 搜索（Go/其他语言的 LookPath 口径，Node 侧等价物是手写 PATH 搜索），
// 其余几条是 Node 侧**把 which 当命令起进程**的既有写法——它们比裸词更具体，
// 单独列出是为了给出更准的失败文案。
var desktopProbePatterns = []forbiddenProbe{
	{regexp.MustCompile(`\bwhich\b`), "不得用 which 探测本机安装（设计 §6.5：解析链是显式的）"},
	{regexp.MustCompile(`command\s+-v`), "不得用 `command -v` 探测本机安装（设计 §6.5）"},
	{regexp.MustCompile(`LookPath`), "不得用 PATH 搜索（LookPath 口径）去找底座——那会静默命中用户自己装的那份"},
	{regexp.MustCompile(`execFile\(\s*['"]which['"]`), "不得把 which 当命令起进程（execFile）"},
	{regexp.MustCompile(`execFileSync\(\s*['"]which['"]`), "不得把 which 当命令起进程（execFileSync）"},
	{regexp.MustCompile(`spawnSync\(\s*['"]which['"]`), "不得把 which 当命令起进程（spawnSync）"},
	{regexp.MustCompile(`spawn\(\s*['"]which['"]`), "不得把 which 当命令起进程（spawn）"},
}

// TestDesktopPayloadResolutionMustNotProbeHost 桌面壳的载荷解析不得探测本机安装（M3-4）。
//
// 失败信息给到 文件名 + 行号 + 命中片段（行内容），便于直接跳过去改。
func TestDesktopPayloadResolutionMustNotProbeHost(t *testing.T) {
	repoRoot := filepath.Join(frontendRoot(t), "..")

	// ① fail-closed：先确认扫描对象都在。缺任何一个都判失败——文件被改名/删除/尚未落地时，
	// 守卫若"扫不到就当通过"就彻底失去意义。
	var missing []string
	for _, rel := range desktopPayloadFiles {
		path := filepath.Join(repoRoot, rel)
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, fmt.Sprintf("  %s（%v）", rel, err))
		}
	}
	if len(missing) > 0 {
		t.Fatalf("桌面载荷解析的源文件不存在，守卫没有可检查的对象（fail-closed：不因改名/删除静默通过）：\n%s\n"+
			"若解析链换了文件，请同步 desktopPayloadFiles", strings.Join(missing, "\n"))
	}

	// ② 逐个文件剥注释后逐行扫描。
	var violations []violator
	for _, rel := range desktopPayloadFiles {
		path := filepath.Join(repoRoot, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读 %s 失败: %v", rel, err)
			continue
		}
		violations = append(violations, scanProbeLines(rel, string(raw))...)
	}
	if len(violations) > 0 {
		lines := make([]string, 0, len(violations))
		for _, v := range violations {
			lines = append(lines, "  "+v.String())
		}
		t.Errorf("桌面壳的载荷解析出现本机安装探测（%d 处）：\n%s\n"+
			"后果是静默换人：机器上恰好装过底座时，用户那份会顶掉随包那份，而 /meta 的 payloads[] 仍报成随包命中。"+
			"解析链必须只认显式配置 / 随包载荷 / 壳自己按显式规则算出的路径（设计 §6.5）。",
			len(violations), strings.Join(lines, "\n"))
	}

	// ③ 机制自检：这条守卫的价值全在"剥注释 + 命中口径"这两个机械动作上，它们写错了
	// 要么全绿（漏报，如把整行当注释切掉）要么全红（误报，如中文注释里提到 which）。
	// 用内存样本自证，不依赖被扫文件的内容（子测试不占顶层守卫条数）。
	t.Run("剥注释与命中口径自检", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want []int // 期望命中的行号
		}{
			{"裸 which 命中", "const p = which('dsh')\n", []int{1}},
			{"行注释里的 which 不算", "// 不要用 which 探测本机安装\nconst a = 1\n", nil},
			{"块注释里的 which 不算", "/* 不要用 which（中文说明） */\nconst a = 1\n", nil},
			{"块注释跨行后行号仍准", "/* 注释\n   还是注释 */\nconst p = which('x')\n", []int{3}},
			{"command -v 命中", "execSync('command -v dsh')\n", []int{1}},
			{"command    -v 也命中", "run('command    -v dsh')\n", []int{1}},
			{"LookPath 命中", "const p = LookPath('dsh')\n", []int{1}},
			{"execFileSync 起 which 命中", "execFileSync('which', ['dsh'])\n", []int{1}},
			{"spawn 起 which 命中", "spawn(\"which\", ['dsh'])\n", []int{1}},
			{"URL 里的 // 不吞掉后面的代码", "const u = 'https://example.test/x'\nwhich('dsh')\n", []int{2}},
			{"命中多行", "which('a')\n// which('b')\nLookPath('c')\n", []int{1, 3}},
		}
		for _, tc := range cases {
			got := scanProbeLines("sample.ts", tc.src)
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

// scanProbeLines 扫描一段 TS/JS 源码（**先剥注释**），返回逐行的违规（行号按原文计）。
func scanProbeLines(rel, src string) []violator {
	var out []violator
	for i, line := range strings.Split(stripTSComments(src), "\n") {
		for _, p := range desktopProbePatterns {
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

// stripTSComments 去掉 TS/JS 的行注释与块注释，**保持行号不变**：
// 注释内容被替换成等量空格（换行保留），因此扫描到的行号与原文一致。
//
// 为什么顺带跳过字符串字面量：这两个文件里会出现 URL（`https://…`）与正则/路径，
// 只看 `//` 会把它们后半段当注释切掉——那样守卫可能漏掉真正的代码（切掉的正是要扫的部分）。
// 引号状态机只处理 `'` `"` 反引号三种引号与反斜杠转义，够这两个文件用（TS 模板串里的
// `${…}` 按普通文本处理：其中若出现被禁止的写法仍会被扫到，宁可多报不可漏报）。
func stripTSComments(src string) string {
	out := []byte(src)
	const (
		stCode = iota
		stLineComment
		stBlockComment
		stSingle
		stDouble
		stBacktick
	)
	state := stCode
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch state {
		case stCode:
			switch {
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				out[i], out[i+1] = ' ', ' '
				i++
				state = stLineComment
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				state = stBlockComment
			case c == '\'':
				state = stSingle
			case c == '"':
				state = stDouble
			case c == '`':
				state = stBacktick
			}
		case stLineComment:
			if c == '\n' {
				state = stCode
				continue
			}
			out[i] = ' '
		case stBlockComment:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = stCode
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
		case stSingle, stDouble, stBacktick:
			// 字符串按原样保留（不参与注释剥离）；只识别配对的引号与反斜杠转义。
			if c == '\\' {
				i++
				continue
			}
			switch {
			case state == stSingle && c == '\'':
				state = stCode
			case state == stDouble && c == '"':
				state = stCode
			case state == stBacktick && c == '`':
				state = stCode
			}
		}
	}
	return string(out)
}
