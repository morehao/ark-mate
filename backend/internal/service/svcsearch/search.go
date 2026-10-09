// Package svcsearch 定义跨会话消息检索面的服务端口。
package svcsearch

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtosearch"
)

// SearchSvc 消息检索面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ SearchSvc = (*T)(nil) 保证一致性。
type SearchSvc interface {
	// SearchMessages 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转）。
	SearchMessages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error)
}
