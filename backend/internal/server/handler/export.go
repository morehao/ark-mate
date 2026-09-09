package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// ExportHandler 处理会话批量导出（M3 ②：JSON / 纯文本 / Markdown）。
type ExportHandler struct {
	svc Service
}

// Conversations 批量导出：POST /conversations/export
// body {ids: [...], format?: "markdown"|"text"|"json"}（ids 上限 50）。
func (h *ExportHandler) Conversations(c *gin.Context) {
	var req ExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.ExportConversations(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, h.svc.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}
