package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ModelsHandler 处理模型目录 REST（GET /models?agent=<id>）。
//
// 同时依赖两个端口：模型目录来自底座面（svcruntime），而"未指定 agent 时取哪个"
// 属于 Agent 管理面的知识（svcagent.DefaultAgentID）——窄接口让这层依赖显式可见。
type ModelsHandler struct {
	svc      svcruntime.RuntimeSvc
	agentSvc svcagent.AgentSvc
}

// List 返回某 agent 的运行时派生模型目录。
//
// 目录来源是运行时（ACP session config options），不是平台内置清单：
// 无 agent 参数时取默认 agent（dsh），以便面板首屏直接可用。
func (h *ModelsHandler) List(c *gin.Context) {
	agentID := c.Query("agent")
	if agentID == "" {
		agentID = h.agentSvc.DefaultAgentID()
	}
	resp, err := h.svc.ModelCatalog(c.Request.Context(), agentID)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
