package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ModelsHandler 处理模型目录 REST（GET /models?agent=<id>）。
type ModelsHandler struct {
	svc Service
}

// List 返回某 agent 的运行时派生模型目录。
//
// 目录来源是运行时（ACP session config options），不是平台内置清单：
// 无 agent 参数时取默认 agent（dsh），以便面板首屏直接可用。
func (h *ModelsHandler) List(c *gin.Context) {
	agentID := c.Query("agent")
	if agentID == "" {
		agentID = h.svc.DefaultAgentID()
	}
	resp, err := h.svc.ModelCatalog(c.Request.Context(), agentID)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
