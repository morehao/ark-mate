package interaction

import "context"

// Callbacks 拨号时挂给执行面的**平台形状**回调集合（权限 / 结构化提问 / 提问完成）。
//
// 为什么定义在本域：三个回调的载荷值（`PermissionRequest` / `PermissionDecision` /
// `ElicitationRequest` / `ElicitationOutcome`）都是本域的类型。会话域不能 import 本域
// （`TestDomainPackagesMustNotImportEachOther`），所以它只把这份集合当**不透明载荷**转发
// （`session.DialOptions.Hooks`），适配器按本类型断言、再翻译成协议回调。
//
// 纪律与 `event.Signal` 一致：**平台值在域内，协议形状在适配器**。用例层因此不必 import 协议库。
type Callbacks struct {
	// OnPermission 一次权限征询（阻塞等待用户决策）。nil = 未接线（适配器会以取消/拒绝兜底）。
	OnPermission func(ctx context.Context, req PermissionRequest) PermissionDecision
	// OnElicitation 一次结构化提问（阻塞等待用户回答）。
	OnElicitation func(ctx context.Context, req ElicitationRequest) ElicitationOutcome
	// OnElicitationComplete 提问已在底座侧完成的收敛通知（URL 模式）。
	OnElicitationComplete func(ctx context.Context, elicitationID string)
}
