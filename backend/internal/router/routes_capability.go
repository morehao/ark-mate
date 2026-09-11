package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrcapability"
)

// registerCapability P1 能力数据面：Skill/MCP/Tool 目录（platform 唯一事实源，REST 列出能力），
// 以及 MCP 的 live 交互（探测 / 工具调用 / 资源 / 提示词）。
//
// live 面均为显式触发、不做隐式启动探测（行为不回归）；资源与提示词 live 拉取，不落盘/不目录化。
// 工具名与提示词名不含 "/"——gin 路径参数不匹配斜杠。
func registerCapability(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctrcapability.NewCapabilityCtr(ports.Capability)

	v1.GET("/skills", ctr.ListSkills)
	// §13.4 S4：启停（overlay：写回定义文件的 enabled + 立刻重校物化，禁用即撤回已物化副本）。
	v1.PATCH("/skills/:id", ctr.SetSkillEnabled)
	// §13.4 S2：物化体检（只读）：期望摘要 vs 实测摘要 + status，回答"agent 实际读到的与注册表一致吗"。
	v1.GET("/skills/:id/materialization", ctr.SkillMaterialization)
	v1.GET("/mcps", ctr.ListMcpServers)
	v1.GET("/tools", ctr.ListTools)
	// P2 §5.2 MCP live 探测：tools/list → 写 Tool 目录。
	v1.POST("/mcps/:id/probe", ctr.ProbeMcpServer)
	// B1：MCP 工具调用（tools/call，独立会话）。
	v1.POST("/mcps/:id/tools/:tool/call", ctr.CallMcpTool)
	// B2：MCP 资源与提示词。
	v1.GET("/mcps/:id/resources", ctr.ListMcpResources)
	v1.POST("/mcps/:id/resources/read", ctr.ReadMcpResource)
	v1.GET("/mcps/:id/prompts", ctr.ListMcpPrompts)
	v1.POST("/mcps/:id/prompts/:name/get", ctr.GetMcpPrompt)
}
