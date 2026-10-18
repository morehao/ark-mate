// Package code 收敛 ark-mate 对外 REST 信封的业务错误码。
//
// 结构对齐 ark-iam 的 pkg/code：code.go 放注册表，领域码按文件分开。
// 但**编号规则是 ark-mate 自己的**：ark-iam 是 6 位「领域段」码（1001XX…），
// ark-mate 沿用既有的 5 位 HTTP 语义码（40001/40401/…）——这批数字已经是
// 对外契约（README 接口摘要、前端 response.ts、大量既有测试都按它断言），
// 本次收敛只把它们搬到一处并消除重复，**不改编号**。
//
// 信封约定（golib gincontext）：成功 code=0；业务错误 code!=0 且 HTTP 状态仍为
// 200；`code: -1` 是"没有携带业务码的裸错误"落到 golib 兜底分支的产物，属于
// 我们要消灭的形态——凡经 FromError 出口的错误都带码。
package code

import (
	"errors"
	"fmt"

	"github.com/morehao/golib/gerror"

	"github.com/morehao/ark-mate/internal/redact"
)

// 成功码。gincontext 的成功分支同样落 0。
const OK = 0

// 请求侧错误码（4xxxx）：调用方可以按 msg 自行纠正。
const (
	// InvalidParam 参数缺失/类型或取值不合法（控制器做了入参校验后返回）。
	InvalidParam = 40000
	// InvalidRequest 请求语义非法：游标互斥、归属不符、状态机不允许等
	// ——参数本身格式没问题，但在这条资源上不成立。
	InvalidRequest = 40001
	// Unauthorized 单操作员令牌缺失或无效（HTTP 状态同时为 401）。
	Unauthorized = 40101
	// NotFound 目标资源不存在（会话/agent/runtime/provider/MCP server…）。
	NotFound = 40401
	// NotSupported 该能力在当前形态下明确不支持，且不做臆造降级
	// （典型：memory 存储模式下的 /usage/summary）。
	NotSupported = 40501
	// Conflict 与当前状态冲突或超出额度（在途轮次未收敛、上游超时等）。
	Conflict = 40901
)

// 服务侧错误码（5xxxx）：调用方无法通过改参数解决。
const (
	// Internal 未归类的内部错误（兜底码，绝不外抛 -1）。
	Internal = 50000
	// UpstreamFailed 上游（ACP runtime / MCP server）返回了错误或异常终态。
	UpstreamFailed = 50001
	// BadGateway 与上游的连接建立/传输失败（进程拉不起、连接断开、不可达）。
	BadGateway = 50201
)

// defaultMessages 每个码的兜底文案。
//
// 只用于两类场景：错误本身没带 msg（FromError 兜底）、以及需要给码配一条
// 可读说明的地方（诊断日志、契约测试）。绝大多数业务错误自带更精确的 msg，
// 会覆盖这里的默认值——所以这里只保证"任何码都有一句话解释"。
var defaultMessages = gerror.CodeMsgMap{
	OK:             "success",
	InvalidParam:   "参数错误",
	InvalidRequest: "请求非法",
	Unauthorized:   "未授权",
	NotFound:       "资源不存在",
	NotSupported:   "当前形态不支持该能力",
	Conflict:       "与当前状态冲突",
	Internal:       "服务内部错误",
	UpstreamFailed: "上游执行失败",
	BadGateway:     "上游连接失败",
}

// Message 返回某个码的默认文案（未知码返回空串）。
func Message(c int) string { return defaultMessages[c] }

// Codes 返回全部已登记的码，供契约测试与文档校验使用。
func Codes() []int {
	out := make([]int, 0, len(defaultMessages))
	for c := range defaultMessages {
		out = append(out, c)
	}
	return out
}

// Error 构造带业务码的错误。
//
// 这是控制器/领域层表达"对外错误"的唯一构造入口：直接写字面量
// gerror.Error{Code: 40001, Msg: …} 会让码散落各处，改码时无法全局检索。
func Error(c int, msg string) gerror.Error {
	if msg == "" {
		msg = Message(c)
	}
	// 脱敏闸门：对外文案可能夹带上游原文（如 runtime 报错里回显的 key）。
	// 这是控制面所有对外错误的唯一构造入口（FromError 与 fromACL 都汇到这里）。
	return gerror.Error{Code: c, Msg: redact.String(msg)}
}

// Errorf 是 Error 的格式化版本。
func Errorf(c int, format string, args ...any) gerror.Error {
	return Error(c, fmt.Sprintf(format, args...))
}

// FromError 把任意 error 归一成**一定携带业务码**的错误。
//
// 控制器出口用它，从而杜绝 golib gincontext 的 `code: -1` 兜底形态
// （buildErrorResponse 对不带 gerror.Error 的错误填 -1，客户端拿不到可判定的码）。
//
// 映射顺序：
//  1. 已经是带码的 gerror.Error → 原样透传（避免二次映射把 40401 压成 50000）；
//  2. 是 acl 分类错误 → 按 Class 映射（见 aclClassCode）；
//  3. 其余 → Internal，msg 取 err.Error()。
//
// nil 原样返回 nil，便于 `return code.FromError(err)` 的直写。
func FromError(err error) error {
	if err == nil {
		return nil
	}
	var coded gerror.Error
	if errors.As(err, &coded) {
		return err
	}
	if c, ok := fromACL(err); ok {
		return c
	}
	return Error(Internal, err.Error())
}
