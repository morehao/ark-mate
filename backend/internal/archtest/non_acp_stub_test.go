package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNonACPBottomStubStaysProtocolFree C2 §3.4 的验收桩必须**存在、且不认识协议**。
//
// 判据背后的事实：C2 把协议从内圈清干净之后，`grep` 只能证明 import 表干净，**证明不了
// "换掉实现还能跑"**。机器证明是 `internal/bootstrap/app/stub_execution_test.go` 里的纯内存
// 执行面桩——它顶替 `runtimeconn` 跑 `TestControlPlaneContractBaseline` 与会话事件快照。
//
// 两种静默失效各有一条判据：
//
//  1. **桩被删/改名**：判据还在，但"换底座不动框架"不再有证明，而所有测试照样绿。所以下面
//     一段是 fail-closed 的文件定位 + 结构断言（端口实现缺一个即红）。
//  2. **桩悄悄 import 协议库**：那样它就不再是"非 ACP 桩"，证明退化成"用另一份协议实现跑通"
//     ——内圈到底还认不认识协议，又没人知道了。
func TestNonACPBottomStubStaysProtocolFree(t *testing.T) {
	root := backendRoot(t)
	rel := "internal/bootstrap/app/stub_execution_test.go"
	path := filepath.Join(root, rel)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("非 ACP 桩 %s 不在了：C2 §3.4 的验收（换底座不动框架）随之消失，请恢复它", rel)
	}
	src := string(raw)

	file, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", rel, err)
	}
	if len(file.Imports) == 0 {
		t.Fatalf("%s 解析不到任何 import：判据失去对象", rel)
	}
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if p == modulePrefix+"internal/adapters/outbound/acp" {
			t.Errorf("%s import 了协议库 `internal/adapters/outbound/acp`：它必须是**非 ACP** 的桩，"+
				"否则「换底座不动框架」这条证明退化成「用另一份协议实现跑通」", rel)
		}
	}

	// 结构断言：桩必须真的实现那四个端口（少一个说明它已经不完整，验收也就跟着变弱）。
	for _, want := range []string{
		"session.ConnPool",
		"session.Conn",
		"session.AgentConn",
		"session.AgentSession",
	} {
		if !strings.Contains(src, "var _ "+want+" =") {
			t.Errorf("%s 缺少 `var _ %s = ...` 的实现断言：端口少实现一个，桩就不成立", rel, want)
		}
	}
	// 两条验收用例必须还在（判据是它们，不是桩本身）。
	for _, name := range []string{
		"TestStubExecutionRunsControlPlaneContractBaseline",
		"TestStubExecutionSessionEventSnapshot",
	} {
		if !strings.Contains(src, "func "+name+"(") {
			t.Errorf("%s 缺少验收用例 %s：桩跑通契约基线/事件快照这件事没有判据", rel, name)
		}
	}
}
