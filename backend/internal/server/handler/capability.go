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
