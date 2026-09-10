package handler

import (
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/internal/service/svccapability"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// CapabilityHandler 处理能力数据面 REST（P1：Skill/MCP/Tool 目录列出；注入属 P2）。
type CapabilityHandler struct {
	svc svccapability.CapabilitySvc
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

// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：结果写 Tool 目录并
// 返回本轮增删。失败时错误码进信封（并已记录 last_probe_error，可由 GET /mcps 读回）。
// CallMcpTool 调用 MCP server 的工具（B1：tools/call）。
func (h *CapabilityHandler) CallMcpTool(c *gin.Context) {
	var req dtocapability.McpToolCallReq
	// 空 body 合法（= 无参数调用）；非空但非法才拒绝。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.CallMcpTool(c.Request.Context(), c.Param("id"), c.Param("tool"), req)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpResources 拉取 MCP server 的资源列表（B2：resources/list + resources/templates/list）。
func (h *CapabilityHandler) ListMcpResources(c *gin.Context) {
	resp, err := h.svc.ListMcpResources(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// ReadMcpResource 读取一个资源（B2：resources/read）。
func (h *CapabilityHandler) ReadMcpResource(c *gin.Context) {
	var req dtocapability.McpResourceReadReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.ReadMcpResource(c.Request.Context(), c.Param("id"), req.URI)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpPrompts 拉取 MCP server 的提示词模板（B2：prompts/list）。
func (h *CapabilityHandler) ListMcpPrompts(c *gin.Context) {
	resp, err := h.svc.ListMcpPrompts(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// GetMcpPrompt 取用一条提示词模板（B2：prompts/get）。
func (h *CapabilityHandler) GetMcpPrompt(c *gin.Context) {
	var req dtocapability.McpPromptGetReq
	// 空 body 合法（= 无参数）；非空但非法才拒绝。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.GetMcpPrompt(c.Request.Context(), c.Param("id"), c.Param("name"), req)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

func (h *CapabilityHandler) ProbeMcpServer(c *gin.Context) {
	resp, err := h.svc.ProbeMcpServer(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
