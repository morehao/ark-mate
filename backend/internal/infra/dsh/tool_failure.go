package dsh

import "strings"

// 工具失败的两类签名（v27 / S4-③）。
//
// **来源是实测**（2026-10-16 的探针，见 `.dsh/docs/notes/probes/workbuddy-alignment-probe-notes.md`）：
//
//	被沙箱拒绝    status:"completed" + `[sandbox: file access denied under workspace-write mode]`
//	              （+ 升权提示）。**status 完全看不出来**——它说这一条"跑完了"。
//	沙箱后端坏了  status:"failed" + `… no sandbox backend is usable on this host …`
//	              `… Runner failure: spawn … ENOENT`
//
// 另外一条实测结论决定了这里**不能**怎么写：结构化的 `SANDBOX_UNAVAILABLE` 在
// `tool/result.data.error.code` 里，而 ACP 的 `tool_call_update` **只转发 `message.content`**，
// 所以那串字连文本里都不出现——**不得**把它写成机器判据（写了就是一条永远匹配不上的判据，
// 而"永远不匹配"和"今天没有这种情况"长得一模一样）。
const (
	// FailureSandboxDenied 这条工具调用被**沙箱边界**拒了（可以引导升权：换成允许写的工作空间/模式）。
	FailureSandboxDenied = "sandbox_denied"
	// FailureSandboxUnavailable 宿主的沙箱后端本身不可用（**不能**引导升权：再点一次也一样失败，
	// 得换平台/装依赖）。与上一条分开是刻意的——两者的处置完全相反。
	FailureSandboxUnavailable = "sandbox_unavailable"

	// CodeSandboxWriteDenied 与底座的措辞逐字对齐的稳定码（`[sandbox: file access denied …]`）。
	CodeSandboxWriteDenied = "sandbox_file_access_denied"
)

// ToolFailure 实现 `runtime.ToolFailureClassifier`（dsh 的工具结果文本归类）。
//
// 编不过 `var _ runtime.ToolFailureClassifier = ...` 是**刻意的**：adapter 不能 import 根包
// （根包要 import 它来登记，双向就成环）。接口按方法集结构满足，编译期断言只能放在根包那一侧
// ——编译期断言在**登记点**：`registerBuiltinToolFailures` 把 `ToolFailure{}` 传进收
// `runtime.ToolFailureClassifier` 的 `RegisterToolFailureClassifier`，方法集不满足就编不过。
type ToolFailure struct{}

// ClassifyToolFailure 按签名表归类；不认识 → 空串（**不猜**）。
func (ToolFailure) ClassifyToolFailure(text string) (reason, bizCode string) {
	if text == "" {
		return "", ""
	}
	// 顺序有意义：先认"后端坏了"，因为它的文本里也可能出现 `sandbox`（而那条不能引导升权）。
	if strings.Contains(text, "no sandbox backend is usable") ||
		strings.Contains(text, "Runner failure: spawn") {
		return FailureSandboxUnavailable, ""
	}
	if strings.Contains(text, "file access denied") && strings.Contains(text, "sandbox") {
		return FailureSandboxDenied, CodeSandboxWriteDenied
	}
	return "", ""
}
