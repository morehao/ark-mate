package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// RuntimeHandler 处理 Runtime 相关 REST 请求。
type RuntimeHandler struct {
	svc Service
}

// List 列出可用 Runtime。
func (h *RuntimeHandler) List(c *gin.Context) {
	runtimes, err := h.svc.ListRuntimes()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, runtimes)
}
