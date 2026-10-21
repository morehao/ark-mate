package assembly

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"
	"github.com/morehao/ark-mate/internal/runtime"
)

// AllAdapters 全部内置 runtime adapter（**装配根是唯一认识具体 adapter 的地方**，v3.1 §3.2.1 规则 5）。
//
// 接第二个底座 = 在这个切片里加一行（外加它自己的目录与端口实现），不动框架、不建 Driver
// 全局注册表、不用 init、不写匿名 import。
func AllAdapters() []runtime.Adapter {
	return []runtime.Adapter{dsh.New()}
}

// BuiltinRegistry 全部内置 adapter 的注册表（诊断 / 测试 / "看全部适配面"用）。
//
// 与 `ProductRegistry` 的区别是**语义**：产品装配只看构建期选中的那一个；体检与守卫必须看到
// 全部适配面，否则"被下线底座留下的东西"会在报告里整段消失。
func BuiltinRegistry() *runtime.Registry {
	return runtime.NewAdapterRegistry(AllAdapters()...)
}

// BuiltinSpecs 全部内置 adapter 的声明（诊断面只读事实）。
func BuiltinSpecs() []runtime.Spec { return runtime.SpecsOf(AllAdapters()...) }
