package apphost

import "github.com/morehao/ark-mate/internal/core/failure"

// 错误码 → failure.Class 的映射（T-C3.2：错误映射到现有 failure 类）。
//
// 分类口径（对齐 `internal/core/failure` 的七个 Class）：host RPC 的失败最终服务"用户该做什么"。
//   - 协议/参数/工具名错 → ClassRequest（用户改参数/调用正确工具即可）
//   - 工具执行失败 → ClassService（app 侧内部错误，用户改不了，报给我们/看日志）
//   - slot apply 失败 → ClassService（同上）
//   - api_version 不兼容 → ClassUnsupported（该包不支持当前核心，需升级包或降级核心）
//   - host 崩溃 / 超时 / 响应过大 → ClassConnection（连接层故障，可重试）
//   - 内部错 → ClassService
//
// 未知 code 保守归 ClassUnknown（不假称可重试），fail-closed。
func ClassOfCode(code string) failure.Class {
	switch code {
	case ErrCodeUnknownMethod, ErrCodeBadParams, ErrCodeUnknownTool, ErrCodeUnknownImpl:
		return failure.ClassRequest
	case ErrCodeToolFailed, ErrCodeSlotFailed, ErrCodeInternal:
		return failure.ClassService
	case ErrCodeAPIVersionMismatch:
		return failure.ClassUnsupported
	case ErrCodeResponseTooLarge, ErrCodeTimeout, ErrCodeHostGone:
		return failure.ClassConnection
	default:
		return failure.ClassUnknown
	}
}

// ClassOfRPCError 优先用 host 侧建议的 class（若它是合法 Class），否则按 code 推断。
//
// 用 host 建议的 class 是给 app 一个「我知道这是哪类错」的表达口（如工具明确返回 unsupported）；
// 但它可能是脏值，故校验合法性后回退到 ClassOfCode——不因一个坏 class 字段把错误分类搞乱。
func ClassOfRPCError(e *RPCError) failure.Class {
	if e == nil {
		return failure.ClassUnknown
	}
	if c := failure.Class(e.Class); isValidClass(c) {
		return c
	}
	return ClassOfCode(e.Code)
}

// isValidClass 报告 c 是否是 failure 的合法分类取值。
func isValidClass(c failure.Class) bool {
	switch c {
	case failure.ClassConnection, failure.ClassAccess, failure.ClassLimit,
		failure.ClassRequest, failure.ClassService, failure.ClassUnsupported, failure.ClassUnknown:
		return true
	default:
		return false
	}
}

// AsFailure 把一个 host RPC 错误帧折算成 `*failure.Error`（携带 class + 稳定 code + 人话），
// 供上层与内核其它失败一视同仁地处理。
func AsFailure(e *RPCError) *failure.Error {
	if e == nil {
		return failure.NewError(failure.ClassUnknown, ErrCodeInternal, "host 返回未知错误", nil)
	}
	return failure.NewError(ClassOfRPCError(e), e.Code, e.Message, nil)
}
