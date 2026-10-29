package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// 请求上下文边界的可执行判据（2026-10-27，计划见
// `.dsh/docs/plans/2026-10-27-context-boundary-plan.md`）。
//
// 口径：**handler 写 `c`，service 写 `ctx`**。gin 的 `*gin.Context` 是池化复用对象
// （`gin.Engine.pool` + `Context.reset()`），只允许活在入站适配层的一次请求里；
// 一旦进到 service 及更内层，或者被 goroutine 捕获，就会指向被后续请求改写的对象。
//
// 这两条判据补的是既有 gin 围栏没覆盖的那一半：
//
//	TestGinConfinedToInboundAdaptersV32（layers_v32_test.go）判 **import**——挡"引包"；
//	本文件判 **类型使用**——挡"签名里写类型名"与"goroutine 捕获"。
//
// 二者不替代：一个包完全可以在不 import gin 的前提下，通过参数名/类型别名把 gin 类型
// 带进来；反过来，import 了 gin 的入站包也可能把 `c` 逃逸出去。

// ginContextType 是 gin 上下文类型的字面写法（守卫按**类型语法**判定，不按字符串）。
const ginContextType = "*gin.Context"

// innerLayers 是"gin 类型不得出现"的内层目录（相对 backend/internal）。
//
// 刻意不含 `internal/bootstrap`：装配根需要把控制器交下来，装配期提到 gin 是合理的
// （计划 §7 未决问题 2 的结论）。只有 controller / middleware / router 三个入站带例外。
var innerLayers = []string{
	"service",
	"sessionruntime",
	"domain",
	"persistence",
	"infra",
	"drivers",
	"core",
	"runtime",
	"config",
}

// inboundLayers 是允许出现 gin 的入站适配带。
var inboundLayers = []string{
	"controller",
	"middleware",
	"router",
}

// TestGinTypeStaysInInboundAdapters 守护"service 及内层零 `*gin.Context`"。
//
// 为什么这条比"零 `.Request.Context()`"更本质：前者是**类型边界**，而后者只是写法约定。
// 只要 service 签名里没有 gin 类型，service 内部就无从持有池化对象；仓库里也不会再出现
// "传 `c` 还是 `c.Request.Context()`"的判断题——与形参类型匹配的实参只有纯 `context.Context`
// 和实现了它的具体类型，而后者无法越过签名进入内层。
func TestGinTypeStaysInInboundAdapters(t *testing.T) {
	root := internalRoot(t)
	var violations []string

	for _, layer := range innerLayers {
		dir := filepath.Join(root, layer)
		for _, path := range productionGoFiles(t, dir) {
			file := parseGoOrFatal(t, path)
			ast.Inspect(file, func(n ast.Node) bool {
				star, ok := n.(*ast.StarExpr)
				if !ok {
					return true
				}
				if sel, ok := star.X.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "gin" && sel.Sel.Name == "Context" {
						violations = append(violations, rel(root, path)+" 使用了 "+ginContextType)
					}
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		t.Errorf("内层出现了 %s（%d 处）：\n  %s\n"+
			"规则是「handler 写 c，service 写 ctx」：service 签名一律 `ctx context.Context`，"+
			"gin 类型只许出现在 %v 三个入站带。`*gin.Context` 是池化复用对象，进到内层后可能"+
			"指向被后续请求改写的对象（`Context.reset()` 不清 `c.Request`，失败是偶发且静默的）。",
			ginContextType, len(violations), joinLines(violations), inboundLayers)
	}
}

// TestGinContextDoesNotEscapeToGoroutine 守护"handler 里的 `c` 不被 goroutine 捕获"。
//
// 这是 TestGinTypeStaysInInboundAdapters 的另一半：入站带**允许**出现 gin 类型，
// 但请求返回后 `c` 会被 `reset()` 并放回池中复用，所以它不能活到请求之外。
// 长活任务必须用 `context.WithoutCancel(...)` 派生纯 ctx 再交给 goroutine。
//
// 用 AST 而不是字符串匹配：`req := c.Request; go func(){ h.svc.X(req.Context()) }()`
// 这类间接写法能绕过字符串判据，但绕不过"闭包里引用了 `*gin.Context` 形参"这个语法事实。
func TestGinContextDoesNotEscapeToGoroutine(t *testing.T) {
	root := internalRoot(t)
	var violations []string

	for _, layer := range inboundLayers {
		dir := filepath.Join(root, layer)
		if !dirExists(dir) {
			continue
		}
		for _, path := range productionGoFiles(t, dir) {
			file := parseGoOrFatal(t, path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				escaped := ginParamsEscapingToGoroutine(fn)
				for _, name := range escaped {
					violations = append(violations, rel(root, path)+": "+fn.Name.Name+" 的形参 "+name+" 被 goroutine 捕获")
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("gin 上下文逃逸到 goroutine（%d 处）：\n  %s\n"+
			"`*gin.Context` 是池化复用的：请求返回后会被 `reset()` 并交给下一个请求。"+
			"长活任务请先派生纯 ctx（`context.WithoutCancel(c.Request.Context())` 或"+
			"golib 的 `gincontext.AsyncContext(c)`），再把**纯 ctx** 交给 goroutine，不要捕获 `c`。",
			len(violations), joinLines(violations))
	}
}

// ginParamsEscapingToGoroutine 返回 fn 中类型为 `*gin.Context` 且被函数体内任一
// goroutine 字面量捕获的形参名。
//
// 判定口径：`go` 语句的直接实参若是函数字面量，收集该字面量里引用到的标识符；
// 与 gin 形参名求交。只覆盖"字面量直接传给 go"这一种形态——`go named()` 里没有任何
// 捕获，不会有问题；而把 `c` 塞进结构体再交给 goroutine 属于刻意规避，交给 review。
//
// **已知局限（如实记录，别指望这条守卫兜住）**：先转手再捕获抓不到，例如
//
//	req := c.Request
//	go func() { _ = req }()
//
// 逃逸的对象是 `*http.Request` 而非 `*gin.Context`，语法上已无 gin 形参引用。
// 2026-10-27 的反证 3 确认它**不报**。真正的兜底是 TestGinTypeStaysInInboundAdapters：
// 只要内层签名里没有 gin 类型，这类转手最多把 `*http.Request` 带进 goroutine，
// 不会把池化的 `*gin.Context` 带进去。
func ginParamsEscapingToGoroutine(fn *ast.FuncDecl) []string {
	ginParams := map[string]bool{}
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			if !isGinContextExpr(field.Type) {
				continue
			}
			for _, name := range field.Names {
				ginParams[name.Name] = true
			}
		}
	}
	if len(ginParams) == 0 {
		return nil
	}

	escaped := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		lit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(lit.Body, func(inner ast.Node) bool {
			id, ok := inner.(*ast.Ident)
			if ok && ginParams[id.Name] {
				escaped[id.Name] = true
			}
			return true
		})
		return true
	})

	var out []string
	for name := range escaped {
		out = append(out, name)
	}
	return out
}

// isGinContextExpr 判断表达式是否是 `*gin.Context`。
func isGinContextExpr(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "gin" && sel.Sel.Name == "Context"
}

// internalRoot 返回 backend/internal 的绝对路径（守卫都在 internal/archtest 下运行）。
func internalRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("解析 internal 根目录失败：%v", err)
	}
	return root
}

// parseGoOrFatal 用 AST 解析一个 Go 文件；解析失败说明守卫自身失效，直接停下。
func parseGoOrFatal(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（守卫无法判定）：%v", path, err)
	}
	return file
}

// dirExists 判断目录是否存在（入站带的子目录名是固定的，缺失不该让守卫静默空转）。
func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}
