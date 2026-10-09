// Package testutil 提供跨包复用的测试夹具（只应由 _test.go 引用）。
//
// 存在的理由：同一批基础设施夹具在多个包里重复手写。仅 manager 一个包内，
// `runtime.NewRegistry(runtime.Spec{ID: "stub", Name: "Stub"})` 这个字面量就出现过 20 余次，
// 临时 sqlite 库的「打开 + 错误检查」出现过 12 次。重复的 Spec 字面量一旦与 pkg/runtime
// 的真实字段漂移，测试就会去验证一个并不存在的 runtime 形态，而每个重复点都在各自的
// 文件里，没有一处能代表"我们测试里用的 stub 长这样"。
//
// **本包不得 import internal/manager**——这不是风格偏好，是 Go 的硬约束，已实测：
// manager 的测试是内部测试包（`package manager`），而内部测试包 import 一个反向
// import 它自己的包会直接编译失败（`import cycle not allowed in test`，换外部测试包
// `package manager_test` 才允许）。因此这里只放 manager 无关的夹具；manager 专属的组合
// （Service + Orchestrator 装配）留在 backend/internal/manager/testrig_test.go。
// 这条约束由 internal/archtest 的 TestTestutilMustNotImportImplementations 守卫。
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// StubRegistry 只注册一个**测试专用 stub runtime** 的注册表。
//
// 为什么不叫 echo：echo 演示底座已随 M0 删除（连同内置清单里的它）；测试夹具从来不需要
// 它的实现，只需要一个「稳定存在、从不拉进程」的 runtime id，故改名为 stub——名字本身
// 就说明它是夹具而不是产品 runtime，避免后来人以为「测试还在用 echo」。
//
// 测试刻意用 stub-only 而不是 runtime.DefaultRegistry()：后者带 opencode 与
// deepseekharness，会让「没指定 runtime 的 agent 回落到谁」这类断言依赖内置清单的完整
// 内容——内置清单增删一个底座，一堆与它无关的测试跟着红。需要多底座的用例自己
// runtime.NewRegistry(...) 显式声明。
func StubRegistry() *runtime.Registry {
	return runtime.NewRegistry(runtime.Spec{ID: "stub", Name: "Stub"})
}

// TempDBPath 在当前测试的临时目录里给一个 sqlite 库路径（`<t.TempDir()>/forge.db`）。
//
// 返回**路径**而不是 *store.Store：重启恢复类测试需要在同一路径上先后打开两个句柄
// （关掉第一个再开第二个，模拟进程重启），只给句柄就没法表达这件事。也要注意不要写成
// 每次现算的表达式——t.TempDir() 每次调用都返回**不同**目录，同一测试里算两次就再也
// 打不开同一个库了，所以路径必须先取到变量。
func TempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "forge.db")
}

// OpenStore 打开 path 处的 sqlite 库，失败即终止当前测试。
//
// 有意**不**注册 t.Cleanup(Close)：多数调用点要自己控制关闭时机（重启恢复测试要在同一
// 路径上开第二个句柄、只读测试要显式关掉再重开），再叠一层 cleanup 会变成双重关闭。
// 调用方自己 defer Close / t.Cleanup(Close)。
func OpenStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开测试库 %s: %v", path, err)
	}
	return st
}
