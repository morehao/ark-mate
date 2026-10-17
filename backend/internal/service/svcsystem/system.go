// Package svcsystem 定义**系统面**的服务端口（ADR-2：由 svcexport 并入）。
//
// 它的判据是"跨会话的、与某一个会话无关的动作"：导出把多个会话的 transcript 与事件事实源
// 汇成一份交付件，读的是**全库**而不是某条记录。
package svcsystem

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoexport"
)

// SystemSvc 系统面的服务端口。
//
// 实现方以编译期断言 var _ SystemSvc = (*T)(nil) 保证一致性。
type SystemSvc interface {
	// ExportSessions 批量导出会话（M3 ②：wire transcript/事件事实源 → JSON/文本/Markdown）。
	ExportSessions(ctx context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error)
}
