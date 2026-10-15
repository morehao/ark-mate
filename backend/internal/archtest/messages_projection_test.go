package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v23「消息白名单投影」的守卫（G5）。
//
// 设计 §D3 的判据：`messages` 是 events 的**白名单投影**——只有 `session.user_message` 与
// `session.message` 两类事件成行，其余（thought / tool_* / usage / permission_* / config_options /
// run.* / input_* / session.created…）一律不成行；且**空正文不产行**。
//
// 为什么这条需要机器判据（三种静默长回来的方式，每一种都能编过、跑通、看起来正常）：
//
//	① `default:` 分支又去 `&msgRowBuilder{…}` 开一个空行「承载时间线」——于是每一轮都多出一条
//	   空 assistant 行：检索少命中、上下文计数偏高、界面上多一个空气泡，而没有任何测试变红；
//	② `flush()` 里的空正文判断被删（或挪到 `append` 之后）——纯 thought 的一步就留下一行
//	   空正文（`mate doctor` 的"空正文"一段会开始报数，而人只会以为读模型脏了）；
//	③ 白名单被"顺手扩一条"（例如让 tool_* 进 `events_json`）——`messages` 的含义悄悄变了，
//	   而它能编过、能跑，只有"检索命中了思考文本"这种用户侧现象能暴露它。
//
// 判据落在 **AST** 上（不是文本）：`buildMessageRows` 的 switch 里
//   ① `default` 分支**不得**出现 `&msgRowBuilder{` 复合字面量，且必须以 `continue` 结束；
//   ② 除 default 外的 `case` 表达式集合必须**恰好等于**声明面 `messageRowSourceTypes`
//      （两者的字面量逐字相同）——②同时把"白名单搬家"变成红；
//   ③ `flush()` 里必须存在"正文为空就返回"的判断（`text == ""` 的比较在 append 之前）。
// 解析不到函数/switch/闭包（改名、搬家）→ **fail-closed** 判红，绝不允许静默空转。

// projectionSource 是守卫从源码里读到的投影事实。
type projectionSource struct {
	// cases 是 switch 里出现的 case 表达式字面量（不含 default），升序去重。
	cases []string
	// declared 是 `messageRowSourceTypes` 初始化列表里的元素字面量，升序去重。
	declared []string
	// defaultBuildsRow：default 分支里出现了 `&msgRowBuilder{`。
	defaultBuildsRow bool
	// defaultContinues：default 分支以 `continue` 结束（或至少含一条 continue）。
	defaultContinues bool
	// flushDropsEmpty：flush 闭包里有对 `text` 与 `""` 的比较，且在 append 之前。
	flushDropsEmpty bool
}

// parseMessagesProjection 解析 `internal/manager/messages_materialize.go` 并读出上面那份事实。
func parseMessagesProjection(t *testing.T) projectionSource {
	t.Helper()
	root := backendRoot(t)
	path := filepath.Join(root, "internal", "manager", "messages_materialize.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（白名单投影守卫的事实源读不到）: %v", rel(root, path), err)
	}

	var out projectionSource
	var buildFound bool
	var flushFn *ast.FuncLit

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			// 声明面：`var messageRowSourceTypes = []string{A, B}`。
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name.Name != "messageRowSourceTypes" || i >= len(vs.Values) {
						continue
					}
					cl, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok {
						t.Fatalf("`messageRowSourceTypes` 不是复合字面量（守卫读不到白名单声明）")
					}
					for _, el := range cl.Elts {
						out.declared = append(out.declared, identLiteral(t, el))
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name != "buildMessageRows" {
				continue
			}
			buildFound = true
			flushFn = findFuncLit(d.Body, "flush")
			if flushFn == nil {
				t.Fatalf("`buildMessageRows` 里找不到 `flush` 闭包（守卫的前提消失，fail-closed）")
			}
			sw := findSwitch(d.Body)
			if sw == nil {
				t.Fatalf("`buildMessageRows` 里找不到 switch（白名单投影的形状变了，fail-closed）")
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil { // default
					for _, s := range cc.Body {
						if s == nil {
							continue
						}
						if _, ok := s.(*ast.BranchStmt); ok {
							out.defaultContinues = true
						}
						ast.Inspect(s, func(n ast.Node) bool {
							cl, ok := n.(*ast.CompositeLit)
							if !ok {
								return true
							}
							if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "msgRowBuilder" {
								out.defaultBuildsRow = true
							}
							return true
						})
					}
					continue
				}
				for _, e := range cc.List {
					out.cases = append(out.cases, identLiteral(t, e))
				}
			}
		}
	}
	if !buildFound {
		t.Fatalf("`buildMessageRows` 不见了（守卫的事实源消失，fail-closed）")
	}
	if len(out.cases) == 0 || len(out.declared) == 0 {
		t.Fatalf("没读到白名单（cases=%v declared=%v）——守卫会静默空转，判红", out.cases, out.declared)
	}

	// ③ flush：必须有一条「正文为空 → 直接返回」的判断，且出现在任何 `append(out, …)` 之前。
	//
	// 判据刻意收紧到**条件就是那个比较**（而不是"表达式树里出现过 `text == ""`"）：
	// `if false && b.text == ""`、`if b.text == "" {}`（空体）都拦不住空行，而它们在文本上
	// 与正确写法长得几乎一样——反证就是这么把本条判据第一次写空转的。
	var appends []token.Pos
	emptyReturn := token.NoPos
	ast.Inspect(flushFn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.IfStmt:
			if isEmptyTextCond(v.Cond) && returnsEmpty(v.Body) {
				if emptyReturn == token.NoPos || v.Pos() < emptyReturn {
					emptyReturn = v.Pos()
				}
			}
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" {
				appends = append(appends, v.Pos())
			}
		}
		return true
	})
	for _, p := range appends {
		if emptyReturn != token.NoPos && emptyReturn < p {
			out.flushDropsEmpty = true
			break
		}
	}
	sort.Strings(out.cases)
	sort.Strings(out.declared)
	return out
}

// TestMessagesAreWhitelistProjection（G5，v23）钉住白名单投影的三条形状。
func TestMessagesAreWhitelistProjection(t *testing.T) {
	src := parseMessagesProjection(t)

	// ① default 分支不得开行（"承载时间线"那个诱惑）。
	if src.defaultBuildsRow {
		t.Fatalf("`buildMessageRows` 的 `default:` 分支里出现了 `&msgRowBuilder{…}`：\n" +
			"非对话事件（thought/tool_*/usage/permission_*/config_options/run.*/input_*…）**不产生消息行**——\n" +
			"让它们开一个空行会在每一轮留下空 assistant 行（检索少命中、上下文计数偏高、界面上多一个空气泡），\n" +
			"而时间线本来就由 events 读面渲染（设计 §D3）")
	}
	if !src.defaultContinues {
		t.Fatalf("`buildMessageRows` 的 `default:` 分支没有 `continue`——它必须显式跳过，" +
			"而不是靠「什么都不做」（少了这句，下一个人往里加一句 append 就没有任何东西拦他）")
	}

	// ② 执行面 == 声明面（两处必须逐字相同，否则 `mate doctor` 的 read-models 体检会按一份
	//    过时的白名单判"库里有没有越界来源"）。
	if strings.Join(src.cases, ",") != strings.Join(src.declared, ",") {
		t.Fatalf("投影的 switch 与 `messageRowSourceTypes` 不是同一份白名单：\n"+
			"  switch      %v\n  声明面      %v\n"+
			"白名单是**一件事**，两处写法必须同时改（doctor 的 read-models 体检用声明面判越界来源）",
			src.cases, src.declared)
	}
	// 反向：白名单本身不许悄悄变宽（加一条会让 `messages` 的含义变掉）。
	want := []string{"BusMessage", "BusUserMessage"}
	if strings.Join(src.declared, ",") != strings.Join(want, ",") {
		t.Fatalf("白名单被改了：%v（期望 %v）——\n"+
			"`messages` 只投影「可检索/可寻址的对话正文」两类事件；扩白名单等于改变读模型的含义"+
			"（检索会开始命中思考或工具文本）。真要扩，必须同时改：投影 switch、`messageRowSourceTypes`、"+
			"设计文档 §D3、以及 `mate doctor` 的 read-models 判据", src.declared, want)
	}

	// ③ 空正文不产行。
	if !src.flushDropsEmpty {
		t.Fatalf("`flush` 里没有「正文为空就丢弃」的判断（或它在 append 之后）：\n" +
			"没有正文的行在检索里永远不命中、在界面上是一个空气泡、在「空正文」体检里报一条假阳性——\n" +
			"它是投影的产物，不是事实（设计 §D3）")
	}
}

// identLiteral 取一个 case/lit 元素的字面量名（`BusMessage` → "BusMessage"；其它形态原样打印）。
func identLiteral(t *testing.T, e ast.Expr) string {
	t.Helper()
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	t.Fatalf("白名单里的元素不是标识符（守卫读不懂）：%T", e)
	return ""
}

// findFuncLit 在函数体里找名为 name 的闭包赋值（`flush := func() {…}`）。
func findFuncLit(body *ast.BlockStmt, name string) *ast.FuncLit {
	var found *ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		fl, ok := as.Rhs[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		found = fl
		return false
	})
	return found
}

// findSwitch 找函数体里第一个 switch（`buildMessageRows` 只有一个：被投影事件类型那个）。
func findSwitch(body *ast.BlockStmt) *ast.SwitchStmt {
	var found *ast.SwitchStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if sw, ok := n.(*ast.SwitchStmt); ok {
			found = sw
			return false
		}
		return true
	})
	return found
}

// isTextSelector 判 `x.text` 形态（`cur.text` / `b.text` 都算）。
func isTextSelector(e ast.Expr) bool {
	se, ok := e.(*ast.SelectorExpr)
	return ok && se.Sel.Name == "text"
}

// isEmptyTextCond 判条件**就是** `x.text == ""`（任一侧；不接受它嵌在 `&&`/`||` 里）。
func isEmptyTextCond(e ast.Expr) bool {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.EQL {
		return false
	}
	return (isTextSelector(be.X) && isEmptyString(be.Y)) || (isEmptyString(be.X) && isTextSelector(be.Y))
}

// returnsEmpty 判函数体的第一条语句就是裸 `return`（"丢弃这一行"的那个动作）。
func returnsEmpty(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	return ok && len(ret.Results) == 0
}

// isEmptyString 判 `""` 字面量。
func isEmptyString(e ast.Expr) bool {
	bl, ok := e.(*ast.BasicLit)
	return ok && bl.Kind == token.STRING && bl.Value == `""`
}
