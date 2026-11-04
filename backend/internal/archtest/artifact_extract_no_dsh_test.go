package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestArtifactExtractNoDshTouch（E-3，零侵入守卫）：产物抽取的解析层（`infra/workartifact`）
// **不得** spawn 进程、不得写工作区、不得 import 底座 adapter。
//
// 依据：产物自动抽取是**纯旁路观察者**（方案 §2.5 的 C1/C2）——只读 dsh 已上报的工具事件、
// 只做路径判断，绝不反过来驱动或改动 dsh。这条守卫把它变成机器判据，防止将来有人为图方便
// 在这里调底座进程、或在解析层里顺手写文件。
//
// 三条判据（都按包级 AST 扫描，扫非测试 .go）：
//  1. 不 import `internal/infra/dsh`（不反向依赖具体底座）与 `os/exec`（不 spawn）；
//  2. 不出现 `exec.Command(...)` / `syscall.Exec(...)`（不起进程）；
//  3. 不出现写盘调用 `os.WriteFile/os.Create/os.OpenFile/os.Remove/os.MkdirAll/...`——
//     只许只读（`os.Open`/`os.ReadFile`/`os.Stat`；注意 `os.OpenFile` 可能带写标志，一律禁）。
func TestArtifactExtractNoDshTouch(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal", "infra", "workartifact")
	paths, err := filepathGlobGo(root)
	if err != nil {
		t.Fatalf("扫 %s: %v", root, err)
	}

	// 允许的只读 os 调用白名单；其余 os.* 调用中带写语义的一律禁（见 forbiddenOsCalls）。
	forbiddenImports := []string{
		"internal/infra/dsh",
		"os/exec",
	}
	// 明确禁止的写/起进程调用（选择子全名，packageAlias.Func 形式在下面用 ast.SelectorExpr 匹配）。
	forbiddenCalls := map[string]string{
		"exec.Command":    "不得 spawn 底座进程（C1 只读事件，不注入）",
		"exec.CommandContext": "不得 spawn 底座进程（C1）",
		"syscall.Exec":    "不得 exec（C1）",
		"os.WriteFile":    "不得写工作区文件（C2 只读文件）",
		"os.Create":       "不得写工作区文件（C2）",
		"os.OpenFile":     "不得 OpenFile（可能带写标志）（C2）",
		"os.Remove":       "不得删工作区文件（C2）",
		"os.RemoveAll":    "不得删工作区文件（C2）",
		"os.Mkdir":        "不得建目录（C2）",
		"os.MkdirAll":     "不得建目录（C2）",
		"os.Rename":       "不得移动工作区文件（C2）",
		"os.Chmod":        "不得改工作区权限（C2）",
	}

	scanned := 0
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, path, nil, 0) // 需要 AST 全量（查调用）
		if err != nil {
			t.Fatalf("解析 %s: %v", path, err)
		}
		// ① import 判据
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, fb := range forbiddenImports {
				if p == fb || strings.HasPrefix(p, fb+"/") {
					t.Errorf("%s import 了 %q —— 零侵入：解析层不得依赖底座 adapter / os-exec（E-3）", path, p)
				}
			}
		}
		// ②③ 调用判据：匹配 `pkg.Func` 选择子
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			full := x.Name + "." + sel.Sel.Name
			if why, banned := forbiddenCalls[full]; banned {
				t.Errorf("%s 调用了 %s —— %s（E-3 零侵入守卫）", path, full, why)
			}
			return true
		})
	}

	// fail-closed：一个源文件都没扫到 → 目录改名会让守卫空转。
	if scanned == 0 {
		t.Fatalf("一个 workartifact 源文件都没扫到——目录改名了？E-3 会空转")
	}
}
