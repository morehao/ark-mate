package code

import (
	"errors"
	"fmt"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// RuntimeError 是**上游执行错误**的平台视图：分类 + 稳定 code + 文案 + 诊断尾巴。
//
// 它是内圈与诊断面共用的语言：会话域的状态机、模型面的"这个取值不被支持"、诊断快照都用它。
// 底座协议自己的错误类型（`acp.Error`）只出现在两个地方——协议库内部，以及**这里**的转换。
//
// 为什么不直接用 `acp.Error`：那是线协议的形状（含 SDK/进程/JSON-RPC 的判据），内圈一旦
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

// RuntimeErrorOf 从错误链里取**上游错误视图**：平台视图优先，协议错误折成同形视图。
//
// 为什么两种都认：判"这是不是某类故障"的调用点（`code == "model_value_unsupported"`、
// `class == unsupported`）面对的错误来自两边——平台自己造的平台错误，与协议库/适配器直接
// 返回的协议错误。只认一种，那些分支就会**静默失配**（错误不再被识别成"可降级的那一类"，
// 而是原样往上冒），表现是"以前能用的兜底路径忽然开始报错"。
func RuntimeErrorOf(err error) (*RuntimeError, bool) {
	if err == nil {
		return nil, false
	}
	var re *RuntimeError
	if errors.As(err, &re) {
		return re, true
	}
	if ae, ok := acp.As(err); ok && ae != nil {
		return runtimeErrorFromACP(ae), true
	}
	return nil, false
}

// runtimeErrorFromACP 把一份协议错误折成平台视图（唯一转换点）。
func runtimeErrorFromACP(ae *acp.Error) *RuntimeError {
	return &RuntimeError{
		Class: Class(ae.Class), Code: ae.Code, Details: ae.Details,
		StderrTail: ae.StderrTail, Err: ae.Err,
	}
}

// ClassifyRuntime 把任意错误折成平台视图（nil → nil，永不返回"无分类"）。
//
// 顺序：① 已经是平台视图 → 原样；② 协议/进程/SDK 错误 → 经 `acp.Classify` 得到分类再折
// （那一层认识 JSON-RPC 错误码与 `exec.ExitError`，不能搬进来——它是协议细节）；
// ③ 其余 → `ClassUnknown` + 原文。
func ClassifyRuntime(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	if re, ok := RuntimeErrorOf(err); ok {
		return re
	}
	ae := acp.Classify(err)
	if ae == nil {
		return &RuntimeError{Class: ClassUnknown, Details: err.Error(), Err: err}
	}
	return runtimeErrorFromACP(ae)
}
