package svccapability

import (
	"context"
	"io"

	dommcp "github.com/morehao/ark-mate/internal/domain/mcp"
	domskill "github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// CapabilitySvc 能力数据面用例端口（Skill / MCP server / Tool 目录与 MCP live 交互）。
//
// 为什么这一层现在存在：v3.1 §3.2.1 规则 1 要求 **controller 不得 import `internal/domain`**。
// 这些动作的 DTO 与错误码本来就在域平面里折好（域是事实源），所以用例层只做**窄端口的复述**：
// 每个方法一个签名、一行委托，不做二次投影、不加业务判断。
//
// 这不是"一行转发适配器"那种骚操作——它搬运的是**边**（controller → service → domain），
// 而不是给同一个类型起第二个名字：控制器拿不到 `*skill.Plane`/`*mcp.Plane`，也就调不到
// 写定义、扩权、探测之外的任何东西。
type CapabilitySvc interface {
	// ——— Skill 目录与写面 ———
	ListSkills() ([]dtocapability.SkillResp, error)
	WriteSkill(ctx context.Context, pathID string, req dtocapability.SkillWriteReq, create bool) (*dtocapability.SkillWriteResp, error)
	DeleteSkill(ctx context.Context, id string) (*dtocapability.SkillDeleteResp, error)
	ImportPackage(ctx context.Context, r io.ReaderAt, size int64) (*dtocapability.SkillWriteResp, error)
	SetSkillEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error)
	SkillMaterialization(id string) (*dtocapability.SkillMaterializationResp, error)

	// ——— MCP server 写面与探测 ———
	WriteServer(ctx context.Context, pathID string, req dtocapability.McpWriteReq, create bool) (*dtocapability.McpWriteResp, error)
	DeleteServer(ctx context.Context, id string) (*dtocapability.McpDeleteResp, error)
	Probe(ctx context.Context, id string) (*dtocapability.McpProbeResp, error)

	// ——— MCP live 五条 ———
	CallTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error)
	ListResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error)
	ReadResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error)
	ListPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error)
	GetPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error)

	// ——— 两个读模型（工具目录 / MCP 注册表） ———
	ListTools() ([]dtocapability.ToolResp, error)
	ListMcpServers() ([]dtocapability.McpServerResp, error)
}

// capabilitySvc 是 CapabilitySvc 的实现：持有域平面与两个读模型。
type capabilitySvc struct {
	skills  *domskill.Plane
	mcp     *dommcp.Plane
	tools   ToolCatalog
	catalog McpCatalog
}

// NewCapability 构造能力数据面用例。四个入参都是**依赖事实**（域平面 / 读模型），
// 用例实例在装配根建一次、控制器只收接口。
func NewCapability(skills *domskill.Plane, mcp *dommcp.Plane, tools ToolCatalog, catalog McpCatalog) CapabilitySvc {
	return &capabilitySvc{skills: skills, mcp: mcp, tools: tools, catalog: catalog}
}

func (s *capabilitySvc) ListSkills() ([]dtocapability.SkillResp, error) {
	return s.skills.ListSkills()
}

func (s *capabilitySvc) WriteSkill(ctx context.Context, pathID string, req dtocapability.SkillWriteReq, create bool) (*dtocapability.SkillWriteResp, error) {
	return s.skills.WriteSkill(ctx, pathID, req, create)
}

func (s *capabilitySvc) DeleteSkill(ctx context.Context, id string) (*dtocapability.SkillDeleteResp, error) {
	return s.skills.DeleteSkill(ctx, id)
}

func (s *capabilitySvc) ImportPackage(ctx context.Context, r io.ReaderAt, size int64) (*dtocapability.SkillWriteResp, error) {
	return s.skills.ImportPackage(ctx, r, size)
}

func (s *capabilitySvc) SetSkillEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	return s.skills.SetSkillEnabled(ctx, id, enabled)
}

func (s *capabilitySvc) SkillMaterialization(id string) (*dtocapability.SkillMaterializationResp, error) {
	return s.skills.SkillMaterialization(id)
}

func (s *capabilitySvc) WriteServer(ctx context.Context, pathID string, req dtocapability.McpWriteReq, create bool) (*dtocapability.McpWriteResp, error) {
	return s.mcp.WriteServer(ctx, pathID, req, create)
}

func (s *capabilitySvc) DeleteServer(ctx context.Context, id string) (*dtocapability.McpDeleteResp, error) {
	return s.mcp.DeleteServer(ctx, id)
}

func (s *capabilitySvc) Probe(ctx context.Context, id string) (*dtocapability.McpProbeResp, error) {
	return s.mcp.Probe(ctx, id)
}

func (s *capabilitySvc) CallTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error) {
	return s.mcp.CallTool(ctx, id, tool, req)
}

func (s *capabilitySvc) ListResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error) {
	return s.mcp.ListResources(ctx, id)
}

func (s *capabilitySvc) ReadResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error) {
	return s.mcp.ReadResource(ctx, id, uri)
}

func (s *capabilitySvc) ListPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error) {
	return s.mcp.ListPrompts(ctx, id)
}

func (s *capabilitySvc) GetPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error) {
	return s.mcp.GetPrompt(ctx, id, name, req)
}

func (s *capabilitySvc) ListTools() ([]dtocapability.ToolResp, error) { return s.tools.ListTools() }

func (s *capabilitySvc) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	return s.catalog.ListMcpServers()
}

// MaxSkillPackageBytes 技能包上传的字节上限（**事实源**在 `domain/skill`；
// 这里复述一次是为了让控制器不必 import 领域包，见 v3.1 §3.2.1 规则 1）。
const MaxSkillPackageBytes = domskill.MaxPackageBytes
