// Package svcexport 定义会话导出面的服务端口。
package svcexport

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoexport"
)

// ExportSvc 会话导出面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ ExportSvc = (*T)(nil) 保证一致性。
type ExportSvc interface {
	// ExportConversations 批量导出会话（M3 ②：wire transcript/事件事实源 → JSON/文本/Markdown）。
	ExportConversations(ctx context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error)
}
