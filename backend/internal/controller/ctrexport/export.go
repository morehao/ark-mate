// Package ctrexport 承载会话导出控制器。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrexport

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/store"
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

// NewExportCtr 构造 ExportCtr：交出**三份依赖事实**，在这里造出事实来源与导出用例。
//
// `st`/`plane`/`bus` 由装配侧交出（`st` 在 memory 模式为 nil：`svcsystem.NewSource` 按它
// 选载体），而 `svcsystem.New`/`NewSource` 都在**控制器**里调——`app` 与 `router` 都不该
// import `service/svc*`（`internal/archtest/svc_layer_consumer_test.go` 钉着这条）。
func NewExportCtr(st *store.Store, plane *session.Plane, bus *eventbus.Bus) ExportCtr {
	return &exportCtr{svc: svcsystem.New(svcsystem.NewSource(st, plane, bus))}
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
