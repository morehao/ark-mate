// Package svccapability 定义能力数据面的服务端口：
// Skill / MCP server / Tool 目录，以及 MCP 的 live 探测、资源与提示词。
package svccapability

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// CapabilitySvc 能力数据面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ CapabilitySvc = (*T)(nil) 保证一致性。
type CapabilitySvc interface {

	// ——— 写面（M2）：能力定义可在面板里增删改，**事实源仍是文件** ———
	//
	// 请求体是原文 JSON 对象（`map[string]json.RawMessage`）：`PUT` 的语义是"只改出现过的键"，
	// 键的出现与否是语义而不是编码细节（见 dtocapability.SkillWriteReq 的注释）。
	// 三条纪律对所有写面方法一致：先过注册闸门校验 → 通过才落盘（C9）→ 落盘失败回滚内存。

	// CreateSkill 新建 skill 定义（id 已存在 → `skill_exists` 40901）。
	CreateSkill(ctx context.Context, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error)

	// UpdateSkill 覆盖写 skill 定义（只改请求体里出现的键；路径 id 与 body id 不一致 → 40000）。
	UpdateSkill(ctx context.Context, id string, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error)

	// DeleteSkill 删除 skill 定义并撤回已物化副本（不存在 → `deleted:false` 且无错）。
	DeleteSkill(ctx context.Context, id string) (*dtocapability.SkillDeleteResp, error)

	// CreateMcpServer 新建 MCP server 定义（id 已存在 → `mcp_exists` 40901）。
	CreateMcpServer(ctx context.Context, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error)

	// UpdateMcpServer 覆盖写 MCP server 定义（全部门禁与装载同一条路）。
	UpdateMcpServer(ctx context.Context, id string, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error)

	// DeleteMcpServer 删除 MCP server 定义并一并摘除其归属的工具条目。
	DeleteMcpServer(ctx context.Context, id string) (*dtocapability.McpDeleteResp, error)

	// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测并写 Tool 目录（P2 §5.2）。
	ProbeMcpServer(ctx context.Context, id string) (*dtocapability.McpProbeResp, error)

	// CallMcpTool 调用某 MCP server 的工具（B1：tools/call；独立会话，不做连接池）。
	CallMcpTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error)

	// B2：MCP 资源与提示词（live 拉取，不落盘/不目录化）。
	ListMcpResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error)

	ReadMcpResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error)

	ListMcpPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error)

	GetMcpPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error)
}
