package code

import (
	"errors"
	"fmt"
)

// RuntimeError 是**上游执行错误**的平台视图：分类 + 稳定 code + 文案 + 诊断尾巴。
//
// 它是内圈与诊断面共用的语言：会话域的状态机、模型面的"这个取值不被支持"、诊断快照都用它。
// 协议库自己的错误类型（`acp.Error`）只出现在协议库内部与适配器出口——**适配器在把错误交给
// 上层之前**把它折成本类型（`runtime.FoldError`），本包因此不认识任何协议类型（B4）。
//
// 为什么不直接用协议错误：那是线协议的形状（含 SDK/进程/JSON-RPC 的判据），内圈一旦
// 认识它，换一个非 ACP 底座就要改领域与用例；而"这一轮为什么停了、用户该做什么"是平台事实。
type RuntimeError struct {
	// Class 可操作分类（重试 / 登录 / 改参数 / 降级）。
	Class Class
	// Code 稳定 code 名（进 run.error 载荷与诊断面；前端据此给动作）。
	Code string
	// Details 人话描述（已脱敏；可直接展示）。
	Details string
	// StderrTail 底座子进程 stderr 的最近几行（诊断尾巴；空 = 未采集/无输出）。
	StderrTail []string
	// Err 原始错误（供日志与更深层的判定；不进对外载荷）。
	Err error
}

// Error 实现 error（诊断日志与 `%v` 用）。
func (e *RuntimeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("runtime error: class=%s code=%s %s", e.Class, e.Code, e.Details)
	}
	return fmt.Sprintf("runtime error: class=%s code=%s %s: %v", e.Class, e.Code, e.Details, e.Err)
}

// Unwrap 让 `errors.Is/As` 能穿透到原始错误（判连接类故障时用）。
func (e *RuntimeError) Unwrap() error { return e.Err }

// NewRuntimeError 造一个平台侧的上游错误（诊断面在需要时补 StderrTail）。
func NewRuntimeError(class Class, name, msg string, cause error) *RuntimeError {
	return &RuntimeError{Class: class, Code: name, Details: msg, Err: cause}
}

// RuntimeErrorOf 从错误链里取**平台错误视图**：上游错误视图优先，平台带码错误折成同形视图。
//
// 为什么两种都认：判"这是不是某类故障"的调用点（`code == "model_value_unsupported"`、
// `class == unsupported`）面对的错误来自两边——适配器折回来的上游错误，与内圈自己用
// `NewError` 造的带码错误（例如"投影不支持这个操作"）。只认一种，那些分支就会**静默失配**
// （错误不再被识别成"可降级的那一类"，而是原样往上冒），表现是"以前能用的兜底路径忽然开始报错"。
//
// 取外层：`Coded` 包着一条上游错误时，"这是什么错"的答案是外层那层分类与 code。
func RuntimeErrorOf(err error) (*RuntimeError, bool) {
	if err == nil {
		return nil, false
	}
	if ce, ok := CodedOf(err); ok {
		return &RuntimeError{
			Class: ce.Class, Code: ce.Code, Details: ce.Details,
			StderrTail: ce.StderrTail, Err: ce.Err,
		}, true
	}
	var re *RuntimeError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// ClassifyRuntime 把任意错误折成平台视图（nil → nil，永不返回"无分类"）。
//
// 顺序：① 已是平台错误（上游视图或带码错误）→ 取其视图；② 其余 → `ClassUnknown` + 原文。
//
// 协议/进程/SDK 错误的归类**不在这里**：那一步（认 JSON-RPC 错误码、`exec.ExitError`）是协议
// 细节，随适配器出口一起搬走（`runtime.FoldError`）。走到 ③ 的裸错误说明它没有被任何适配器折过，
// 折成 unknown 而不是硬猜一个分类——猜错会让前端按错误的动作引导用户。
func ClassifyRuntime(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	if re, ok := RuntimeErrorOf(err); ok {
		return re
	}
	return &RuntimeError{Class: ClassUnknown, Details: err.Error(), Err: err}
}
