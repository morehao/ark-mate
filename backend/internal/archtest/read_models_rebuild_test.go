package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 v21「读模型可重建」的守卫（G7）。
//
// 判据 J7：`runs`/`messages` 是 `events` 的投影，**任何时候都能从事件重建**。这条口径的失效
// 方式全是静默的——读模型坏了不会有任何东西报错，而"重建的代码路径"可以有两种坏法：
//
//	① **两份实现**：有人为了"让 doctor 也能重建"另写一条循环（`for sessions { svc.Rebuild… }`）
//	   或另写一个 `RebuildAllReadModels` 的副本。两份实现今天看着一样，明天改一处就分叉，
//	   而分叉的后果是"启动重建出来的"与"doctor 重建出来的"读模型不同——最难查的一类不一致；
//	② **写路径漏出体检的默认路径**：把重建挂在 `runDoctor` 里无条件执行（或把
//	   `checkReadModelRebuild` 的"没开关就拒绝"删掉）。后果是"体检"变成"每次跑都改库"，
//	   而那与 README/AGENTS.md 承诺的"doctor 是只读体检"直接冲突——用户跑一条只读命令却发现
//	   库被改了，是信任问题，不是功能问题。
//
// 所以判据钉三层（都 fail-closed，解析不到就红）：
//
//	① 会话侧 `RebuildSessionReadModels`（**单会话重建的唯一实现**）存在，且**恰好一个调用点**，
//	   那个调用点在 `RebuildAllReadModels`（**唯一的重建循环**，P1a-1b 起落在
//	   `mechanism/doctor`）里；
//	② 那个循环**恰好两个触发面**：启动钩子 `rebuildStaleReadModels` 与 doctor 的
//	   `checkReadModelRebuild`（两侧共用一份实现；多一个 = 有人在别处又写了一段循环）；
//	③ doctor 那一侧：`--rebuild-read-models` 开关真的存在（字段 + flag 注册）、
//	   `runDoctor` 里对它的**条件分支**内调用体检的重建项、且 `checkReadModelRebuild` 自己在
//	   "没给开关"时**早退拒绝**并调用机制层的 `RebuildAllReadModels`。
//
// 判据**按符号**锚定（不钉文件路径）：实现与触发者的落点已经搬过一次
// （`manager/read_model_doctor.go` → `mechanism/doctor/readmodels.go`，P1a-1b），
// 下一次是 `persist.go` → `domain/session`（P3b）——钉路径的话两次都会假红。

// rebuildGuard 是守卫从源码里读到的全部事实。
type rebuildGuard struct {
	// hasImpl `func (s *Service) RebuildSessionReadModels` 存在。
	hasImpl bool
	// implCallers `RebuildSessionReadModels` 的调用点所在函数名（升序，非测试源码）。
	implCallers []string
	// loopCallers `RebuildAllReadModels`（唯一的重建循环）的调用点所在函数名（升序，非测试源码）。
	loopCallers []string
	// hasFlagField `doctorOptions` 里有 `RebuildReadModels` 字段。
	hasFlagField bool
	// hasCLIFlag `fset.Bool("rebuild-read-models", …)` 注册了。
	hasCLIFlag bool
	// gateCallsCheck `runDoctor` 里 `if opts.RebuildReadModels { … }` 内调用了重建检查。
	gateCallsCheck bool
	// checkRefuses `checkReadModelRebuild` 在 `!opts.RebuildReadModels` 时早退。
	checkRefuses bool
	// checkCallsLoop `checkReadModelRebuild` 调用了 `RebuildAllReadModels`（机制层的循环）。
	checkCallsLoop bool
}

// TestReadModelsAreRebuildable（G7，v21）钉住"读模型可重建"这条口径的三层。
func TestReadModelsAreRebuildable(t *testing.T) {
	root := backendRoot(t)
	g := collectRebuildGuard(t, root)

	if !g.hasImpl {
		t.Fatalf("`internal/sessionruntime` 里找不到 `func (s *Service) RebuildSessionReadModels`：\n" +
			"J7 的实现点不见了（守卫的事实源消失，fail-closed）")
	}
	// ① 唯一实现：调用点只许一个，且在唯一的重建循环里。
	if len(g.implCallers) != 1 || g.implCallers[0] != "RebuildAllReadModels" {
		t.Fatalf("`RebuildSessionReadModels` 的调用点必须**恰好一个**且在 `RebuildAllReadModels` 里，"+
			"实际 %v：\n两份重建实现今天看着一样，明天改一处就分叉——而分叉的后果是「启动重建出来的」"+
			"与「doctor 重建出来的」读模型不同（J7 要求重建与触发者无关）", g.implCallers)
	}
	// ② 启动钩子与 doctor 共用同一份循环，且**只有**这两个触发面。
	if len(g.loopCallers) != 2 {
		t.Fatalf("`RebuildAllReadModels` 应恰好被两个消费面调用（启动钩子 + doctor 重建），实际 %v：\n"+
			"少一个 = 有一侧自己另写了一段循环（两份实现的开始）；多一个 = 多了一个能改读模型的入口",
			g.loopCallers)
	}
	joined := strings.Join(g.loopCallers, ",")
	if !strings.Contains(joined, "rebuildStaleReadModels") {
		t.Fatalf("启动钩子 `rebuildStaleReadModels` 没有调用 `RebuildAllReadModels`（实际 %v）：\n"+
			"迁移把 `meta.read_models_rebuild` 置上之后，**唯一**会自动消费它的地方就是这里", g.loopCallers)
	}
	if !strings.Contains(joined, "checkReadModelRebuild") {
		t.Fatalf("doctor 的 `checkReadModelRebuild` 没有调用 `RebuildAllReadModels`（实际 %v）：\n"+
			"命令行这一侧要么没接线，要么自己另写了一段重建（两份实现）", g.loopCallers)
	}

	// ③ doctor 侧：开关存在、被条件分支挡着、且检查自己也会拒绝。
	if !g.hasFlagField || !g.hasCLIFlag {
		t.Fatalf("`--rebuild-read-models` 开关不完整（字段=%v CLI=%v）：\n"+
			"字段存在但 flag 没注册 ⇒ 这条路径用户永远走不到（重建只能靠起一次 serve）",
			g.hasFlagField, g.hasCLIFlag)
	}
	if !g.gateCallsCheck {
		t.Fatalf("`runDoctor` 里没有「`if opts.RebuildReadModels` 才调用重建检查」这个条件分支：\n" +
			"无条件调用 = 体检每次跑都改库，而 doctor 承诺的是**只读体检**")
	}
	if !g.checkRefuses {
		t.Fatalf("`checkReadModelRebuild` 没有「没给开关就早退拒绝」那一句：\n" +
			"调用点接错线时它应当**拒绝写**（记 warn），而不是照写——只读承诺不能只靠一个 if 的位置")
	}
	if !g.checkCallsLoop {
		t.Fatalf("`checkReadModelRebuild` 没有调用 `RebuildAllReadModels`（机制层的那个循环）：\n" +
			"命令行这一侧要么没接线，要么自己另写了一段重建（两份实现）")
	}
}

// collectRebuildGuard 用 go/parser 读出上面那份事实（读的是 AST，不是文本：注释里的同名文字、
// 字符串字面量都不该被误读）。
func collectRebuildGuard(t *testing.T, root string) rebuildGuard {
	t.Helper()
	var g rebuildGuard
	fset := token.NewFileSet()

	// ① 实现与触发者侧：`internal/` 全树的非测试源码（**递归**）。
	//
	// 为什么是整棵树而不是 `internal/sessionruntime/*.go`：实现与两个触发者会随包搬家
	// （`persist.go` → 目标 `internal/domain/session`、`read_model_doctor.go` → 目标
	// `internal/mechanism/doctor`），而 `os.ReadDir` **不递归**——文件一进子目录，扫描要么
	// 静默空转、要么"解析到一些但恰好漏掉关键的那几个"。放宽到整棵树同时是**更强**的判据：
	// 下面 `implCallers` / `loopCallers` 都是**精确值**断言，少一个 = 有触发者被搬走，
	// 多一个 = 有人在别处又写了一段循环（两份实现的开始，正是本守卫挡的第一类坏法）。
	internalDir := filepath.Join(root, "internal")
	parsed := 0
	walkErr := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			// 跳过 testdata 与 archtest 自身：前者不是生产源码，后者的判据字面量就写着这几个
			// 函数名（与 knowledge_boundary_test.go、logging_boundary_test.go 同一手法）。
			if d.Name() == "testdata" || d.Name() == "archtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("解析 %s 失败: %w", rel(root, path), perr)
		}
		parsed++
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fn := fd.Name.Name
			if fd.Recv != nil && fd.Name.Name == "RebuildSessionReadModels" {
				g.hasImpl = true
			}
			if fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch calleeName(call) {
				case "RebuildSessionReadModels":
					g.implCallers = append(g.implCallers, fn)
				case "RebuildAllReadModels":
					g.loopCallers = append(g.loopCallers, fn)
				}
				return true
			})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("递归遍历 %s 失败: %v", rel(root, internalDir), walkErr)
	}
	if parsed == 0 {
		t.Fatalf("`internal/` 下没解析到任何非测试 Go 文件（守卫会静默空转，fail-closed）")
	}

	// ② doctor 侧：开关 + 条件分支。
	//
	// 扫描面跟着实现走：v3.2 刀 10 把 doctor 命令体从 `cliops` 搬进 `internal/maintenance`
	// （命令层只剩分发）。判据仍然是"有开关 + 有早退拒绝 + 调唯一的重建循环"。
	cmdDir := filepath.Join(root, "internal", "maintenance")
	doctorFile := parseGuardFile(t, fset, filepath.Join(cmdDir, "doctor.go"), root)
	rebuildFile := parseGuardFile(t, fset, filepath.Join(cmdDir, "doctor_readmodels.go"), root)

	for _, decl := range doctorFile.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "doctorOptions" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("`doctorOptions` 不是结构体（守卫读不懂，fail-closed）")
				}
				for _, field := range st.Fields.List {
					for _, nm := range field.Names {
						if nm.Name == "RebuildReadModels" {
							g.hasFlagField = true
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			// CLI flag 注册：`fset.Bool("rebuild-read-models", …)`。
			ast.Inspect(d.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || calleeName(call) != "Bool" || len(call.Args) == 0 {
					return true
				}
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"rebuild-read-models"` {
					g.hasCLIFlag = true
				}
				return true
			})
			if d.Name.Name != "runDoctor" {
				continue
			}
			ast.Inspect(d.Body, func(n ast.Node) bool {
				ifs, ok := n.(*ast.IfStmt)
				if !ok || ifs.Cond == nil {
					return true
				}
				if !isRebuildFlagCond(ifs.Cond, false) {
					return true
				}
				ast.Inspect(ifs.Body, func(m ast.Node) bool {
					if call, ok := m.(*ast.CallExpr); ok && calleeName(call) == "checkReadModelRebuild" {
						g.gateCallsCheck = true
					}
					return true
				})
				return true
			})
		}
	}

	for _, decl := range rebuildFile.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "checkReadModelRebuild" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.IfStmt:
				if v.Cond != nil && isRebuildFlagCond(v.Cond, true) {
					g.checkRefuses = true
				}
			case *ast.CallExpr:
				if calleeName(v) == "RebuildAllReadModels" {
					g.checkCallsLoop = true
				}
			}
			return true
		})
	}
	return g
}

func parseGuardFile(t *testing.T, fset *token.FileSet, path, root string) *ast.File {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("读不到 %s（守卫的事实源消失，fail-closed）: %v", rel(root, path), err)
	}
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", rel(root, path), err)
	}
	return file
}

// calleeName 取调用表达式的被调名（`s.Rebuild…()` → "Rebuild…"；裸调 → 函数名）。
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	default:
		return ""
	}
}

// isRebuildFlagCond 判条件是不是（或取反地是）`…RebuildReadModels`。
func isRebuildFlagCond(cond ast.Expr, negated bool) bool {
	if u, ok := cond.(*ast.UnaryExpr); ok && u.Op == token.NOT {
		if !negated {
			return false
		}
		return selectorName(u.X) == "RebuildReadModels"
	}
	if negated {
		return false
	}
	return selectorName(cond) == "RebuildReadModels"
}

// selectorName 取 `a.B` 的 B（非选择器返回空串）。
func selectorName(e ast.Expr) string {
	if se, ok := e.(*ast.SelectorExpr); ok {
		return se.Sel.Name
	}
	return ""
}
