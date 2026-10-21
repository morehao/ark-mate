// Package failure 是**可操作失败分类学**：把一次失败归到"用户该做什么"
// （重试 / 登录 / 改参数 / 降级 / 报给我们），随错误一起上抛，前端据此渲染动作。
//
// 它**负责**：分类词汇（`Class` + 七个取值）、携带分类与稳定 code 的错误类型、
// 从错误链里取回它的入口、以及诊断尾巴的统一裁剪（含脱敏闸门）。
//
// 它**不负责**：把分类折算成对外业务码（那是 `internal/core/code` 的投影表）、
// 以及在协议边界上把 SDK 错误识别成本类型（那是 `internal/infra/protocol/acp` 的 `Classify`）。
//
// 为什么单独成包而不是塞进 `core/code`：后者已有 `func Error(c int, msg string)`，
// 同一包里再放一个 `type Error` 直接冲突；而把类型改名成 `Failure` 放在 `code` 里，
// 会让"码"与"分类"两个词汇在一层里混着读。分开之后两者的依赖是单向的：
// `code → failure`（折算表需要分类），`failure` 不认识码表。
package failure

import (
	"errors"
	"fmt"

	"github.com/morehao/ark-mate/internal/core/redact"
)

// Class 是可操作错误分类（对齐 codeg feedback.rs / AIR 词汇表，随 run.error 上抛，前端据此渲染动作）。
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

// Error 是统一失败：稳定 code + 分类 + 脱敏 details，可继续 Unwrap 原始 error。
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
		return fmt.Sprintf("acp error: class=%s code=%s %s", e.Class, e.Code, e.Details)
	}
	return fmt.Sprintf("acp error: class=%s code=%s %s: %v", e.Class, e.Code, e.Details, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError 构造 *Error。cause 可为 nil。
func NewError(class Class, code, details string, cause error) *Error {
	return &Error{Class: class, Code: code, Details: details, Err: cause}
}

// As 返回 err 链中的 *Error（含直接类型）。
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
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
	// 脱敏闸门：stderr 可能直接打印 key（如 `OPENAI_API_KEY=sk-…`），
	// 这是 stderr 尾巴的唯一出口 → 在此一次性过闸，诊断/事件/错误文案三处消费者都被覆盖。
	return redact.Lines(out)
}
