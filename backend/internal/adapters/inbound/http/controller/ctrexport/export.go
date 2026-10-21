// Package ctrexport 承载会话导出控制器。
//
// 控制器只依赖 internal/app/* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrexport

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtoexport"
	"github.com/morehao/ark-mate/internal/app/system"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ExportCtr 会话导出控制器。
type ExportCtr interface {
	Sessions(c *gin.Context)
}

var _ ExportCtr = (*exportCtr)(nil)

// ExportHandler 处理会话批量导出（M3 ②：JSON / 纯文本 / Markdown）。
type exportCtr struct {
	svc system.SystemSvc
}

// NewExportCtr 构造 ExportCtr（只收**已构造的用例**；事实来源在装配根建）。
func NewExportCtr(svc system.SystemSvc) ExportCtr {
	return &exportCtr{svc: svc}
}

// Sessions 批量导出：POST /sessions/export
// body {ids: [...], format?: "markdown"|"text"|"json"}（ids 上限 50）。
func (h *exportCtr) Sessions(c *gin.Context) {
	var req dtoexport.ExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.ExportSessions(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
