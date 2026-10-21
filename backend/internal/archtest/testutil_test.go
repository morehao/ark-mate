package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestTestutilMustNotImportImplementations 锁定 internal/testutil 的依赖面。
//
// 这条守的是一个 Go 的硬约束，不是风格偏好（已实测）：
//
//	测试包 manager（**内部**测试包，package manager）
//	  └─ import internal/testutil
//	       └─ import internal/app/run        ← 编译失败：import cycle not allowed in test
//
// 若 internal/testutil 反过来 import internal/app/run（很自然的冲动：把
// "造一个 Service + Orchestrator" 的夹具也放进去"统一"一下），manager 的**全部**测试会
// 立刻无法编译，而报错文本（"import cycle not allowed in test"）不会告诉你该怎么改。
// 换外部测试包（package manager_test）确实允许这种环，但 manager 的 29 个测试文件都是内部
// 测试包、且大量直接读写未导出字段（svc.agents、o.conns 等），改外部包不现实。
//
// 所以边界是：testutil 只放 manager 无关的夹具（echo 注册表、临时 sqlite 库），
// manager 专属的组合留在它自己的 `testrig_test.go`（内部测试包的夹具，不跨包复用；
// 重构后随各领域包的测试一起走）。
func TestTestutilMustNotImportImplementations(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "testutil")
	if !hasGoFiles(dir) {
		t.Fatalf("internal/testutil 不存在或无 Go 文件（夹具无处安放？）")
	}
	forbidden := []string{
		modulePrefix + "internal/app/run",
		modulePrefix + "internal/adapters/inbound/http/controller",
		modulePrefix + "internal/adapters/inbound/http/router",
		modulePrefix + "internal/bootstrap/app",
	}
	imports := importedByPackage(t, dir)
	if len(imports) == 0 {
		t.Fatalf("未解析到 internal/testutil 的任何 import（解析失效，守卫形同虚设）")
	}
	for _, imp := range imports {
		for _, bad := range forbidden {
			if imp == bad || strings.HasPrefix(imp, bad+"/") {
				t.Errorf("internal/testutil 不应 import %s：manager 的测试是内部测试包，"+
					"反向依赖会形成 import cycle not allowed in test，manager 全部测试将无法编译", imp)
			}
		}
	}
}
