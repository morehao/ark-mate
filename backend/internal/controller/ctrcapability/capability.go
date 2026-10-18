// Package ctrcapability 承载能力数据面控制器（Skill / MCP server / Tool 目录与 MCP live 交互）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrcapability

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CapabilityCtr 能力数据面控制器（Skill / MCP server / Tool 目录与 MCP live 交互）。
type CapabilityCtr interface {
	ListSkills(c *gin.Context)
	CreateSkill(c *gin.Context)
	UpdateSkill(c *gin.Context)
	DeleteSkill(c *gin.Context)
	ImportSkill(c *gin.Context)
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
//
// P4a 正文第三十九刀之后这里**没有** `svc svccapability.CapabilitySvc` 字段：那个接口已经空了
// （写面 / 探测面 / live 五条都归了域平面），空接口留在控制器上只会让人以为"还有一格端口"。
// 现在持有的全是**依赖事实**（能力注册表 / 技能平面 / MCP 平面 / 密钥面）与本包内构造的两个
// 读模型用例。
type capabilityCtr struct {
	// tools 工具目录**读模型**（P4a 正文第十二刀起在本包内构造）。
	//
	// 它留在用例层而不是让控制器直接用注册表：工具目录要**投影**
	// （`capability.Tool` → DTO，而 `pkg/` 不能 import `internal/dto`）。
	tools svccapability.ToolCatalog
	// skills 技能目录（P4a 正文第十三刀起由域平面直接提供）。
	//
	// 与 `tools` 的区别值得记：技能目录的投影在域平面里（`skill.Plane.ListSkills`
	// 已经把 `capability.Skill` 折成 `dtocapability.SkillResp`，含 warnings 与 scope 判定），
	// 用例层再包一层就只是一行转发——那正是第十一刀判定为骚操作的形状。
	skills *skill.Plane
	// mcpPlane MCP 域平面（P4a 正文第三十七/三十八/三十九刀：写面、探测面、live 五条全在这里，
	// 控制器直接调——DTO 与错误码本来就在域里折好）。
	mcpPlane *mcp.Plane
	// mcp MCP 注册表**读模型**（P4a 正文第十四刀起在本包内构造）。
	//
	// 与 `tools` 同族：条目本体在注册表里、探测态在 MCP 平面里、悬空引用体检在密钥面里，
	// 三个源折成一个响应 ⇒ 投影只能落用例层。
	mcp svccapability.McpCatalog
}

// NewCapabilityCtr 构造 CapabilityCtr（四个参数都是**依赖事实**，用例实例在构造里造）。
func NewCapabilityCtr(caps *capability.Registry, skills *skill.Plane,
	mcpPlane *mcp.Plane, mcpSecrets *mcp.Secrets) CapabilityCtr {
	return &capabilityCtr{
		tools: svccapability.NewTools(caps), skills: skills, mcpPlane: mcpPlane,
		mcp: svccapability.NewServers(caps, mcpPlane, mcpSecrets),
	}
}

// ListSkills 列出 Skill 目录（平台唯一事实源；按 ID 排序）。
func (h *capabilityCtr) ListSkills(c *gin.Context) {
	items, err := h.skills.ListSkills()
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
	resp, err := h.skills.WriteSkill(c.Request.Context(), "", req, true)
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
	resp, err := h.skills.WriteSkill(c.Request.Context(), c.Param("id"), req, false)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteSkill 删除一个 skill 定义并撤回已物化副本（幂等：不存在 → `deleted:false` 且 code=0）。
func (h *capabilityCtr) DeleteSkill(c *gin.Context) {
	resp, err := h.skills.DeleteSkill(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// maxSkillImportBody multipart 信封的上限：20 MiB 包 + 1 MiB 头部/边界余量。
const maxSkillImportBody = skill.MaxPackageBytes + (1 << 20)

// ImportSkill 导入一个技能包（`POST /skills/import`，multipart/form-data，字段 `file`）。
//
// 控制器只做传输层的事：限制请求体大小、读 multipart、把字节交给域平面。解析（front matter、
// id 规范化、zip slip/条目数/压缩比）、写闸门与错误码都在 `domain/skill` 里——上传与手填
// 必须经过**同一条**写面，否则两者对同一份内容会有两种结论。
func (h *capabilityCtr) ImportSkill(c *gin.Context) {
	// 先按请求体上限兜一层：否则一个超大包会先被 gin 读进内存（MaxMultipartMemory 只管内存阈值，
	// 超出的部分会落临时文件），而这里的语义是"超过 20 MiB 直接拒绝"。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSkillImportBody)

	header, err := c.FormFile("file")
	if err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam,
			"缺少 file 字段（multipart/form-data，file=<技能包.zip>）："+err.Error()))
		return
	}
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		gincontext.Fail(c, code.Error(code.InvalidParam, "只接受 .zip 技能包："+header.Filename))
		return
	}
	if header.Size > skill.MaxPackageBytes {
		gincontext.Fail(c, code.Error(code.InvalidParam,
			"技能包超过 20 MiB 上限："+header.Filename))
		return
	}
	f, err := header.Open()
	if err != nil {
		gincontext.Fail(c, code.Error(code.Internal, "读取上传文件失败："+err.Error()))
		return
	}
	defer f.Close()
	// 再按字节数兜一层：`header.Size` 来自 multipart 头，不能当成唯一判据。
	data, err := io.ReadAll(io.LimitReader(f, skill.MaxPackageBytes+1))
	if err != nil {
		gincontext.Fail(c, code.Error(code.Internal, "读取上传文件失败："+err.Error()))
		return
	}
	if len(data) > skill.MaxPackageBytes {
		gincontext.Fail(c, code.Error(code.InvalidParam, "技能包超过 20 MiB 上限"))
		return
	}

	resp, err := h.skills.ImportPackage(c.Request.Context(), bytes.NewReader(data), int64(len(data)))
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
	resp, err := h.mcpPlane.WriteServer(c.Request.Context(), "", req, true)
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
	resp, err := h.mcpPlane.WriteServer(c.Request.Context(), c.Param("id"), req, false)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteMcpServer 删除一个 MCP server 定义，并**一并摘除**归属它的工具条目。
func (h *capabilityCtr) DeleteMcpServer(c *gin.Context) {
	resp, err := h.mcpPlane.DeleteServer(c.Request.Context(), c.Param("id"))
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
	resp, err := h.skills.SetSkillEnabled(c.Request.Context(), id, req.Enabled)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SkillMaterialization 某 skill 的物化体检（§13.4 S2）：每处物化目标的期望摘要 vs 实测摘要、
// 以及 status（in_sync/missing/tampered/stale/disabled/user_owned/not_applicable）。只读。
func (h *capabilityCtr) SkillMaterialization(c *gin.Context) {
	resp, err := h.skills.SkillMaterialization(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpServers 列出 MCP 注册表（按 ID 排序；headers/凭证不读回）。
func (h *capabilityCtr) ListMcpServers(c *gin.Context) {
	items, err := h.mcp.ListMcpServers()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// ListTools 列出 Tool 目录（按 name 排序）。
func (h *capabilityCtr) ListTools(c *gin.Context) {
	items, err := h.tools.ListTools()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// CallMcpTool 调用 MCP server 的工具（B1：tools/call）。
func (h *capabilityCtr) CallMcpTool(c *gin.Context) {
	var req dtocapability.McpToolCallReq
	// 空 body 合法（= 无参数调用）；非空但非法才拒绝。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.mcpPlane.CallTool(c.Request.Context(), c.Param("id"), c.Param("tool"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpResources 拉取 MCP server 的资源列表（B2：resources/list + resources/templates/list）。
func (h *capabilityCtr) ListMcpResources(c *gin.Context) {
	resp, err := h.mcpPlane.ListResources(c.Request.Context(), c.Param("id"))
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
	resp, err := h.mcpPlane.ReadResource(c.Request.Context(), c.Param("id"), req.URI)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListMcpPrompts 拉取 MCP server 的提示词模板（B2：prompts/list）。
func (h *capabilityCtr) ListMcpPrompts(c *gin.Context) {
	resp, err := h.mcpPlane.ListPrompts(c.Request.Context(), c.Param("id"))
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
	resp, err := h.mcpPlane.GetPrompt(c.Request.Context(), c.Param("id"), c.Param("name"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：结果写 Tool 目录并
// 返回本轮增删。失败时错误码进信封（并已记录 last_probe_error，可由 GET /mcps 读回）。
//
// P4a 正文第三十八刀：探测面归域平面——用例层再包一层只会得到一行转发，故控制器直接调
// `h.mcpPlane.Probe`（DTO 与错误码本来就在 `domain/mcp` 里折好）。
func (h *capabilityCtr) ProbeMcpServer(c *gin.Context) {
	resp, err := h.mcpPlane.Probe(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
