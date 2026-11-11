package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— 应用选用贯通（P0/P1）红线守卫：会话能力只有一个写入口 ———
//
// 背景：应用（app）业务工具面的会话级白名单 `CapabilitySelection.Apps` 是**唯一来源**，
// 也是越权红线的守点（app 只能缩小/提供默认，不能借 apps 把会话能力扩张到 session 白名单之外）。
// 它唯一合法的写入通道是 `PATCH /sessions/:id/capabilities`，控制器一律经
// `sessionCtr.UpdateCapabilities` → `svccapability...UpdateCapabilities` 落库。
//
// 这条守卫钉的是「**不要为某个便利功能（如应用的『在新会话用这个应用』按钮）在 PATCH 之外
// 新开一条直写会话能力的接口**」——那会让 `CapabilitySelection.Apps` 出现第二条事实源，绕过三态
// 校验与代际指纹，属于静默越权。本方案（`.dsh/docs/specs/2026-11-11-app-capability-scope-and-launch-design.md`
// §9/§12-3）把它作为构建契约的一部分固定下来。
//
// 判据（两处，都为「恰好一处」）：
//  1. **主牙齿**：控制器层调用 `...UpdateCapabilities(` 的**调用点数必须恰好 1**（只有 `ctrsession` 那个
//     handler）。新增任何第二个「写会话能力」的 handler 都会多一处调用点 → 红。
//  2. **防御纵深**：路由表里对含 `capabilities` 的路径注册的**写方法**（POST/PUT/PATCH）**必须恰好 1 条**
//     ——防「给能力写面换个路径别名再挂一条」。（第 1 条已是主判据；这条只盖路由族，不声称能抓所有旁路。）
//
// fail-closed：扫不到源码即失败（防目录改名/搬家后空转成绿）。反证：在控制器里加第二处
// `UpdateCapabilities(` 调用，或在路由里再注册一条 `POST /sessions/:id/apps` 之类写 capability 的
// 路径，本守卫必须当场变红。

func TestNoSecondCapabilityWriter(t *testing.T) {
	root := backendRoot(t)

	// ① 控制器层：所有 `X.UpdateCapabilities(` 调用点。
	callSites := scanCallSites(t, root, filepath.Join(root, "internal", "controller"), "UpdateCapabilities")
	if len(callSites) != 1 {
		t.Fatalf("会话能力的写入口应恰好 1 处（只有 ctrsession.UpdateCapabilities handler），实际 %d 处：\n%s\n"+
			"若新增第二处直写会话能力，等于在 PATCH /sessions/:id/capabilities 之外开了旁路，越过 CapabilitySelection.Apps 三态与代际指纹——请改走既有 PATCH 通道。",
			len(callSites), strings.Join(callSites, "\n"))
	}
	if !strings.Contains(callSites[0], filepath.Join("ctrsession", "session.go")) {
		t.Fatalf("唯一的 UpdateCapabilities 调用应落在 ctrsession/session.go，实际：%s", callSites[0])
	}

	// ② 路由表：写 `.../capabilities` 的方法注册点。
	routeSites := scanCapabilityWriterRoutes(t, root, filepath.Join(root, "internal", "router"))
	if len(routeSites) != 1 {
		t.Fatalf("能力写入路由应恰好 1 条（PATCH /sessions/:id/capabilities），实际 %d 条：\n%s",
			len(routeSites), strings.Join(routeSites, "\n"))
	}
	if !strings.Contains(routeSites[0], `"/sessions/:id/capabilities"`) {
		t.Fatalf("唯一的写能力路由应是 PATCH /sessions/:id/capabilities，实际：%s", routeSites[0])
	}
}

// scanCallSites 扫 dir 下所有非测试 .go 文件，返回 `expr.UpdateCapabilities(` 调用出现的位置串。
func scanCallSites(t *testing.T, baseRoot, dir, method string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	found := false
	werr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found = true
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("解析 %s 失败: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != method {
				return true
			}
			pos := fset.Position(call.Pos())
			rel, _ := filepath.Rel(baseRoot, pos.Filename)
			out = append(out, rel+":"+itoa(pos.Line))
			return true
		})
		return nil
	})
	if werr != nil {
		t.Fatalf("遍历 %s 失败: %v", dir, werr)
	}
	if !found {
		// fail-closed：这个守卫扫不到任何 .go 就说明扫描目标错了（目录被搬走），不能当绿。
		t.Fatalf("在 %s 下没扫到任何 .go 源文件——目录可能被搬迁/改名，守卫会空转，必须修扫描路径", dir)
	}
	return out
}

// scanCapabilityWriterRoutes 扫 router 包内 `.POST/PUT/PATCH("...capabilities", ...)` 形态的注册。
func scanCapabilityWriterRoutes(t *testing.T, baseRoot, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	found := false
	werr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found = true
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("解析 %s 失败: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "POST", "PUT", "PATCH":
			default:
				return true
			}
			// 第一个实参是路由路径字符串字面量。
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, "/capabilities") {
				pos := fset.Position(call.Pos())
				rel, _ := filepath.Rel(baseRoot, pos.Filename)
				out = append(out, rel+":"+itoa(pos.Line)+" "+sel.Sel.Name+" "+lit.Value)
			}
			return true
		})
		return nil
	})
	if werr != nil {
		t.Fatalf("遍历 %s 失败: %v", dir, werr)
	}
	if !found {
		t.Fatalf("在 %s 下没扫到任何 .go 源文件——目录可能被搬迁/改名，守卫会空转，必须修扫描路径", dir)
	}
	return out
}
