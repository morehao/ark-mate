package assembly

import "fmt"

// ExitError 携带进程退出码的装配错误。
//
// 为什么不是直接 os.Exit：装配层是可被测试与复用的代码，抛错比杀进程可控；
// 退出码策略留在命令层，且与改造前的取值逐一对应（配置类错误 2、运行期错误 1）。
//
// 放在 `internal/assembly` 而不是 `internal/app`：v3.1 的依赖方向是 `cli → app → assembly`，
// 命令层要按退出码收尾，而它只能 import 更低层——错误类型必须住在装配根。
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// AssetErr 资产错误（退出码 3，M2-1）：前端资产缺失/不可用。
//
// 为什么单给一个码：它与"配置写错"（2）和"运行期故障"（1）都不同——**部署形态装错了**。
// 桌面壳据此区分"壳的 --web-dir 指错了"与"核心自己崩了"，也给运维一条可判定的信号。
func AssetErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 3, Err: fmt.Errorf(format, args...)}
}

// UsageErr 用法/配置错误（退出码 2）。
func UsageErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, args...)}
}

// FatalErr 运行期错误（退出码 1）。
func FatalErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 1, Err: fmt.Errorf(format, args...)}
}

// 包内小写别名：装配步骤的历史写法保留（错误码语义以上面的导出函数为准）。
func assetErr(format string, args ...any) *ExitError { return AssetErr(format, args...) }
func usageErr(format string, args ...any) *ExitError { return UsageErr(format, args...) }
func fatalErr(format string, args ...any) *ExitError { return FatalErr(format, args...) }
