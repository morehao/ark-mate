package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// SessionHandler 处理会话相关 REST 请求。
type SessionHandler struct {
	svc Service
}

// List 列出全部会话。
func (h *SessionHandler) List(c *gin.Context) {
	sessions, err := h.svc.ListSessions()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, sessions)
}
