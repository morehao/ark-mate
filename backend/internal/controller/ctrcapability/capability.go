// Package ctrcapability 承载能力数据面控制器（Skill / MCP server / Tool 目录与 MCP live 交互）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrcapability

import (
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CapabilityCtr 能力数据面控制器（Skill / MCP server / Tool 目录与 MCP live 交互）。
type CapabilityCtr interface {
	ListSkills(c *gin.Context)
	CreateSkill(c *gin.Context)
	UpdateSkill(c *gin.Context)
	DeleteSkill(c *gin.Context)
	SetSkillEnabled(c *gin.Context)
	SkillMaterialization(c *gin.Context)
	ListMcpServers(c *gin.Context)
	CreateMcpServer(c *gin.Context)
	UpdateMcpServer(c *gin.Context)
	DeleteMcpServer(c *gin.Context)
	ListTools(c *gin.Context)
	CallMcpTool(c *gin.Context)
	ListMcpResources(c *gin.Context)
	ReadMcpResource(c *gin.Context)
	ListMcpPrompts(c *gin.Context)
	GetMcpPrompt(c *gin.Context)
	ProbeMcpServer(c *gin.Context)
}

var _ CapabilityCtr = (*capabilityCtr)(nil)

// CapabilityHandler 处理能力数据面 REST（P1：Skill/MCP/Tool 目录列出；注入属 P2）。
type capabilityCtr struct {
	svc svccapability.CapabilitySvc
}

// NewCapabilityCtr 构造 CapabilityCtr。
func NewCapabilityCtr(svc svccapability.CapabilitySvc) CapabilityCtr {
	return &capabilityCtr{svc: svc}
}

// ListSkills 列出 Skill 目录（平台唯一事实源；按 ID 排序）。
func (h *capabilityCtr) ListSkills(c *gin.Context) {
	items, err := h.svc.ListSkills()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// CreateSkill 新建一个 skill 定义（M2 写面）：body = skill 定义（`id` 必填）。
// id 已存在 → `skill_exists`(40901)；正文扫描 critical → `definition_invalid`(40001) 且**零字节落盘**。
func (h *capabilityCtr) CreateSkill(c *gin.Context) {
	var req dtocapability.SkillWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateSkill(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateSkill 覆盖写一个 skill 定义：**只改 body 里出现的键**（未出现的保留原值），
// `null` 删键、`""`/`[]` 清空；路径 id 与 body id 不一致 → 40000。
func (h *capabilityCtr) UpdateSkill(c *gin.Context) {
	var req dtocapability.SkillWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateSkill(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteSkill 删除一个 skill 定义并撤回已物化副本（幂等：不存在 → `deleted:false` 且 code=0）。
func (h *capabilityCtr) DeleteSkill(c *gin.Context) {
	resp, err := h.svc.DeleteSkill(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// CreateMcpServer 新建一个 MCP server 定义（M2 写面）：全部门禁与装载同一条路
// （transport 枚举 / stdio 绝对路径 / 密钥引用词法 / 明文与密钥键不重复）。
func (h *capabilityCtr) CreateMcpServer(c *gin.Context) {
	var req dtocapability.McpWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateMcpServer(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateMcpServer 覆盖写一个 MCP server 定义（键的出现与否有语义，同 skill 写面）。
func (h *capabilityCtr) UpdateMcpServer(c *gin.Context) {
	var req dtocapability.McpWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateMcpServer(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteMcpServer 删除一个 MCP server 定义，并**一并摘除**归属它的工具条目。
func (h *capabilityCtr) DeleteMcpServer(c *gin.Context) {
	resp, err := h.svc.DeleteMcpServer(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SetSkillEnabled 启停一个 skill（§13.4 S4）：`{enabled}` → 写回定义文件 + 立刻重校物化。
// 禁用是 overlay（定义与正文保留，可逆），响应回带写回路径与物化计数。
func (h *capabilityCtr) SetSkillEnabled(c *gin.Context) {
	id := c.Param("id")
	var req dtocapability.SkillToggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.SetSkillEnabled(c.Request.Context(), id, req.Enabled)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SkillMaterialization 某 skill 的物化体检（§13.4 S2）：每处物化目标的期望摘要 vs 实测摘要、
// 以及 status（in_sync/missing/tampered/stale/disabled/user_owned/not_applicable）。只读。
func (h *capabilityCtr) SkillMaterialization(c *gin.Context) {
	resp, err := h.svc.SkillMaterialization(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpServers 列出 MCP 注册表（按 ID 排序；headers/凭证不读回）。
func (h *capabilityCtr) ListMcpServers(c *gin.Context) {
	items, err := h.svc.ListMcpServers()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// ListTools 列出 Tool 目录（按 name 排序）。
func (h *capabilityCtr) ListTools(c *gin.Context) {
	items, err := h.svc.ListTools()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：结果写 Tool 目录并
// 返回本轮增删。失败时错误码进信封（并已记录 last_probe_error，可由 GET /mcps 读回）。
// CallMcpTool 调用 MCP server 的工具（B1：tools/call）。
func (h *capabilityCtr) CallMcpTool(c *gin.Context) {
	var req dtocapability.McpToolCallReq
	// 空 body 合法（= 无参数调用）；非空但非法才拒绝。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CallMcpTool(c.Request.Context(), c.Param("id"), c.Param("tool"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpResources 拉取 MCP server 的资源列表（B2：resources/list + resources/templates/list）。
func (h *capabilityCtr) ListMcpResources(c *gin.Context) {
	resp, err := h.svc.ListMcpResources(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ReadMcpResource 读取一个资源（B2：resources/read）。
func (h *capabilityCtr) ReadMcpResource(c *gin.Context) {
	var req dtocapability.McpResourceReadReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.ReadMcpResource(c.Request.Context(), c.Param("id"), req.URI)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpPrompts 拉取 MCP server 的提示词模板（B2：prompts/list）。
func (h *capabilityCtr) ListMcpPrompts(c *gin.Context) {
	resp, err := h.svc.ListMcpPrompts(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// GetMcpPrompt 取用一条提示词模板（B2：prompts/get）。
func (h *capabilityCtr) GetMcpPrompt(c *gin.Context) {
	var req dtocapability.McpPromptGetReq
	// 空 body 合法（= 无参数）；非空但非法才拒绝。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.GetMcpPrompt(c.Request.Context(), c.Param("id"), c.Param("name"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

func (h *capabilityCtr) ProbeMcpServer(c *gin.Context) {
	resp, err := h.svc.ProbeMcpServer(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
