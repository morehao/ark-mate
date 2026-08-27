package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// AgentHandler 处理 Agent 相关 REST 请求。
type AgentHandler struct {
	svc Service
}

// List 列出全部 Agent。
func (h *AgentHandler) List(c *gin.Context) {
	agents, err := h.svc.ListAgents()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, agents)
}
