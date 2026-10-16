// Package svccapability 承载能力数据面的用例与端口：
// Skill / MCP server / Tool 目录，以及 MCP 的 live 交互（资源与提示词）。
package svccapability

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// CapabilitySvc 能力数据面**还没搬完的那半边**的过渡端口。
//
// **剩余形状（P4a 正文第三十八刀之后）**：这里只剩 MCP live 五条。此前陆续搬走的：
//
//	第十二刀 `ListTools`（工具目录读模型 → `Tools`，本包内）
//	第十三刀 `ListSkills`（技能目录的投影本来就在 `domain/skill` 里）
//	第十四刀 `ListMcpServers`（多源折一的读模型 → `Servers`，本包内）
//	第三十六刀 `SetSkillEnabled` / `SkillMaterialization`（投影与错误码都在域平面）
//	第三十七刀 定义写面六条（`WriteSkill` / `WriteServer` / `DeleteServer` 在域平面）
//	第三十八刀 探测面两条 + 一条死转发（`ProbeMcpServer` / `AutoProbeMcpServers` /
//	          `SetMcpProbeTimeout` → `domain/mcp.Plane.Probe` / `.AutoProbe`）
//
// 搬走它们时都不需要新端口：域平面的方法**本来就折好 DTO 与错误码**，用例层再包一层就是
// 一行转发——那正是第三条硬规矩判定为骚操作的形状（见 AGENTS.md「用例层的三条硬规矩」）。
//
// **live 五条搬完时这个接口就空了**——那时它必须与 `router.Ports.Capability`、
// `capabilityCtr.svc` 字段一起删掉：一个空接口在任何地方都能满足，编译器不会提醒任何调用点，
// 留着只会让人以为"能力面还有一个端口"。
type CapabilitySvc interface {
	// CallMcpTool 调用某 MCP server 的工具（B1：tools/call；独立会话，不做连接池）。
	CallMcpTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error)

	// B2：MCP 资源与提示词（live 拉取，不落盘/不目录化）。
	ListMcpResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error)

	ReadMcpResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error)

	ListMcpPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error)

	GetMcpPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error)
}
