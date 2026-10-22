package session

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/core/failure"
)

// classifyFailure 把任意错误归到**可操作分类**（等价于协议层原来的 `Classify`，但域不认识协议）。
//
// 为什么域里还需要一步归一：调用方（REST/WS/编排）拿到的错误既可能是连接边界已经分类过的
// `*failure.Error`，也可能是平台自己包的裸 error；`run.error` 事件要求 `class`/`code` 恒定存在。
//
// 协议边的 SDK 错误映射（JSON-RPC -326xx、进程退出）由连接边界完成——`acp.Client`/`acp.Session`
// 的每个出参都过 `Classify`。所以这里的兜底只需要三件事：已分类 → 原样返回；取消/超时 → 连接类；
// 其余 → unknown（保留原文给用户看）。
func classifyFailure(err error) *failure.Error {
	if err == nil {
		return failure.NewError(failure.ClassUnknown, "unknown", "", nil)
	}
	if ae, ok := failure.As(err); ok {
		return ae
	}
	if errors.Is(err, context.Canceled) {
		return failure.NewError(failure.ClassConnection, "cancelled", "操作已取消", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure.NewError(failure.ClassConnection, "timeout", "操作超时", err)
	}
	return failure.NewError(failure.ClassUnknown, "unknown", err.Error(), err)
}
