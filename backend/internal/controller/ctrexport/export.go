// Package ctrexport 承载会话导出控制器。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrexport

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
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
	svc svcsystem.SystemSvc
}

// NewExportCtr 构造 ExportCtr：交出**事实来源**，在这里造出导出用例。
//
// `src` 由装配侧交出（今天 = `*manager.Service`：sqlite 走库、memory 走会话域镜像 + 事件回放），
// 而 `svcsystem.New` 在**控制器**里调——`app` 与 `router` 都不该 import `service/svc*`
// （`internal/archtest/svc_layer_consumer_test.go` 钉着这条）。
func NewExportCtr(src svcsystem.Source) ExportCtr {
	return &exportCtr{svc: svcsystem.New(src)}
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
