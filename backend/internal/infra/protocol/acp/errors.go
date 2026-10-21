package acp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/morehao/ark-mate/internal/core/failure"
)

// ——— 失败分类学的**事实源已迁到 `internal/core/failure`**（P1，2026-10-21）———
//
// 原来的 `Class`/`Error`/`NewError`/`As`/`TailLines` 与 ACP 无关：全仓 576 处引用里，
// 绝大多数是在给"用户该做什么"分类（重试/登录/改参数/降级），只有本文件底部的
// `Classify` 才真的认识 ACP 的 JSON-RPC 错误码。放在 `infra/acp` 的后果是
// `core` 与 `domain` 为了一个通用词汇而 import 协议包（层表允许、语义不该）。
//
// 这里保留的同名别名只为**既有调用点**读起来自然（`acp.NewError` 在协议层是通顺的），
// 不构成第二事实源：类型只有一处定义，别名不新增任何行为。新代码请直接 import `core/failure`。
type (
	// Class 见 `failure.Class`。
	Class = failure.Class
	// Error 见 `failure.Error`。
	Error = failure.Error
)

// 七个分类取值的转发（与 `failure` 逐字同值）。
const (
	ClassConnection  = failure.ClassConnection
	ClassAccess      = failure.ClassAccess
	ClassLimit       = failure.ClassLimit
	ClassRequest     = failure.ClassRequest
	ClassService     = failure.ClassService
	ClassUnsupported = failure.ClassUnsupported
	ClassUnknown     = failure.ClassUnknown
)

// NewError 见 `failure.NewError`。
var NewError = failure.NewError

// TailLines 见 `failure.TailLines`。
var TailLines = failure.TailLines

// As 见 `failure.As`。
func As(err error) (*Error, bool) { return failure.As(err) }

// stderrDrainWait 是 AttachStderr 快照前等待 stderr 抽干的上限。
// 命中的是"进程退出 → 管道 EOF → 读取 goroutine 收尾"这条快路径，通常立即返回；
// 只有进程仍活着时才真等满这个上限（见 StdioTransport.WaitStderr）。
const stderrDrainWait = 500 * time.Millisecond

// stderrWaiter 由能在快照前告知"stderr 已抽干"的传输实现（当前是 StdioTransport）。
type stderrWaiter interface{ WaitStderr(time.Duration) }

// AttachStderr 把传输层的 stderr 诊断尾巴挂到错误上（已有尾巴则不覆盖；最多保留 tailCap 行）。
//
// 挂之前先等 stderr 抽干（见 snapshotStderr）：不等待的话，"子进程写一行就退出"
// 这类最常见的启动失败会偶发挂上空尾巴——诊断面报"没有 stderr"其实是抢跑读到的。
func AttachStderr(err error, t Transport) error {
	ae, ok := As(err)
	if !ok || ae == nil || t == nil || len(ae.StderrTail) > 0 {
		return err
	}
	ae.StderrTail = snapshotStderr(t)
	return err
}

// snapshotStderr 等 stderr 抽干后再取诊断尾巴——**顺序本身就是正确性**：先取再等等于抢跑，
// "子进程写一行就退出"会拿到空尾巴，调用方据此就会报"底座没有输出"。
//
// 命中"进程退出 → 管道 EOF → 读取 goroutine 收尾"这条快路径时通常立即返回；
// 只有进程仍活着时才真等满 stderrDrainWait。
func snapshotStderr(t Transport) []string {
	if t == nil {
		return nil
	}
	if w, ok := t.(stderrWaiter); ok {
		w.WaitStderr(stderrDrainWait)
	}
	return TailLines(t.StderrTail(), stderrTailCap)
}

// Classify 把任意 error 归类为 *Error；已经是 *Error 则原样返回。
// 主要把 acp-go-sdk 的 *RequestError / *Error 与进程/网络错误映射到可操作分类。
//
// 这是本文件里**唯一认识 ACP 协议**的部分：`-326xx` 是 JSON-RPC 的保留段，
// 其余才是通用失败分类——所以它留在协议层，而分类词汇本身归 `core/failure`。
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	if ae, ok := As(err); ok {
		return ae
	}
	// SDK 的 JSON-RPC 错误响应。
	var re *acpsdk.RequestError
	if errors.As(err, &re) {
		switch re.Code {
		case -32601: // method not found
			return NewError(ClassUnsupported, "method_not_found", re.Message, err)
		case -32602:
			return NewError(ClassRequest, "invalid_params", re.Message, err)
		case -32600:
			return NewError(ClassRequest, "invalid_request", re.Message, err)
		case -32000, -32099: // protocol 保留段（ACP 自定义）按 service 处理
			return NewError(ClassService, "protocol_error", re.Message, err)
		default:
			if re.Code >= 0 { // 业务错误码段：runtime 自定义
				return NewError(ClassService, fmt.Sprintf("runtime_error_%d", re.Code), re.Message, err)
			}
			return NewError(ClassUnknown, "jsonrpc_error", re.Message, err)
		}
	}
	// acpsdk.Error 是协议载荷结构体（无 Error() 方法），不作为 error 链目标；其内容已含于 RequestError。
	// 进程/网络层故障。
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return NewError(ClassConnection, "process_exited", "agent 进程异常退出", err)
	}
	if errors.Is(err, context.Canceled) {
		return NewError(ClassConnection, "cancelled", "操作已取消", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(ClassConnection, "timeout", "操作超时", err)
	}
	return NewError(ClassUnknown, "unknown", err.Error(), err)
}
