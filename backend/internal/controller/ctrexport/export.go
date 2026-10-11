// Package ctrexport 承载会话导出控制器。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrexport

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/service/svcexport"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ExportCtr 会话导出控制器。
type ExportCtr interface {
	Sessions(c *gin.Context)
}

var _ ExportCtr = (*exportCtr)(nil)

// ExportHandler 处理会话批量导出（M3 ②：JSON / 纯文本 / Markdown）。
type exportCtr struct {
	svc svcexport.ExportSvc
}

// NewExportCtr 构造 ExportCtr。
func NewExportCtr(svc svcexport.ExportSvc) ExportCtr {
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
