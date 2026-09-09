package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// CapabilityHandler 处理能力数据面 REST（P1：Skill/MCP/Tool 目录列出；注入属 P2）。
type CapabilityHandler struct {
	svc Service
}

// ListSkills 列出 Skill 目录（平台唯一事实源；按 ID 排序）。
func (h *CapabilityHandler) ListSkills(c *gin.Context) {
	items, err := h.svc.ListSkills()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, items)
}

// ListMcpServers 列出 MCP 注册表（按 ID 排序；headers/凭证不读回）。
func (h *CapabilityHandler) ListMcpServers(c *gin.Context) {
	items, err := h.svc.ListMcpServers()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, items)
}

// ListTools 列出 Tool 目录（按 name 排序）。
func (h *CapabilityHandler) ListTools(c *gin.Context) {
	items, err := h.svc.ListTools()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, items)
}

// AgentCapabilities 返回某 Agent 的统一能力目录（§8.1）：按候选 runtime 聚合 skill/MCP/tool
// 的可见面。未知 agent → 40401（gincontext.Fail 透传 gerror 码）。
func (h *CapabilityHandler) AgentCapabilities(c *gin.Context) {
	resp, err := h.svc.AgentCapabilities(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
