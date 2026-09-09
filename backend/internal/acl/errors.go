package acl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Class 是可操作错误分类（对齐 codeg feedback.rs / AIR 词汇表，随 turn.error 上抛，前端据此渲染动作）。
type Class string

const (
	ClassConnection  Class = "connection"       // 拉起/stdio/连接层故障（可重试）
	ClassAccess      Class = "access"           // 鉴权/权限不足（登录）
	ClassLimit       Class = "limit"            // 配额/并发限制
	ClassRequest     Class = "request_rejected" // 请求被拒（参数/语义错误）
	ClassService     Class = "service"          // runtime 侧内部错误
	ClassUnsupported Class = "unsupported"      // 该 runtime 能力不支持
	ClassUnknown     Class = "unknown"
)

// Error 是 acl 层统一错误：稳定 code + 分类 + 脱敏 details，可继续 Unwrap 原始 error。
type Error struct {
	Class   Class
	Code    string
	Details string
	Err     error
	// StderrTail 失败时子进程 stderr 的最近若干行（诊断尾巴；空 = 未采集/无输出）。
	// 传输层在「进程起不来/握手失败」路径上采集，供上层 REST/事件外露（A3）。
	StderrTail []string
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("acl error: class=%s code=%s %s", e.Class, e.Code, e.Details)
	}
	return fmt.Sprintf("acl error: class=%s code=%s %s: %v", e.Class, e.Code, e.Details, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError 构造 acl.Error。cause 可为 nil。
func NewError(class Class, code, details string, cause error) *Error {
	return &Error{Class: class, Code: code, Details: details, Err: cause}
}

// AttachStderr 把传输层的 stderr 诊断尾巴挂到错误上（已有尾巴则不覆盖；最多保留 tailCap 行）。
func AttachStderr(err error, t Transport) error {
	ae, ok := As(err)
	if !ok || ae == nil || t == nil || len(ae.StderrTail) > 0 {
		return err
	}
	ae.StderrTail = TailLines(t.StderrTail(), stderrTailCap)
	return err
}

// TailLines 截取最近 n 行（超出保留尾部），供诊断外露统一裁剪。
func TailLines(lines []string, n int) []string {
	if n <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// As 返回 err 链中的 *Error（含直接类型）。
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// Classify 把任意 error 归类为 *Error；已经是 *Error 则原样返回。
// 主要把 acp-go-sdk 的 *RequestError / *Error 与进程/网络错误映射到可操作分类。
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
