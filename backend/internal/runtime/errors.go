package runtime

import (
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// ——— 协议错误 → 平台视图的唯一折叠点（B4）———
//
// 口径：**分类（可操作语义）在所有层都是平台词汇（`code.Class`）**，协议词汇（`acp.Class`）
// 只活在协议库与适配器内部。`platform/code` 因此对协议零依赖；把协议错误折成平台视图这一步，
// 由"把错误交给上层"的那一层负责——今天这条出口只有本包的拨号底座与
// `drivers/runtimeconn`（会话/轮次调用），两者都在这里取值。
//
// 为什么不是每个适配器各写一遍：`acp.Classify` 认识 JSON-RPC 错误码段与 `exec.ExitError`，
// 漏写一处或写歪一处的表现是**静默**的——本该 40401/50201 的错误退化成 50000，编译器帮不上，
// 只有按码断言的用例会红。所以折叠逻辑只有一份。
//
// 幂等：已经是平台错误的原样返回（适配器可以放心在每一层出口都调一次）。

// FoldError 把协议/进程层错误折成平台错误视图；已是平台错误则原样返回。
func FoldError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := code.RuntimeErrorOf(err); ok {
		return err
	}
	ae := acp.Classify(err)
	if ae == nil {
		return err
	}
	re := code.NewRuntimeError(code.Class(ae.Class), ae.Code, ae.Details, ae.Err)
	re.StderrTail = ae.StderrTail
	return re
}
