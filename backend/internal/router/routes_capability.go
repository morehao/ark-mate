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
	// M2 写面：能力定义可在面板里增删改，**事实源仍是文件**（<CapabilityDir>/skills/<id>.json）。
	// POST 已存在 → skill_exists(40901)；PUT 只改 body 里出现的键（未出现的保留原值，null 删键）；
	// DELETE 幂等（不存在 → deleted:false）。三者都走注册闸门，坏定义 → definition_invalid(40001) 且零字节落盘。
	v1.POST("/skills", ctr.CreateSkill)
	v1.PUT("/skills/:id", ctr.UpdateSkill)
	v1.DELETE("/skills/:id", ctr.DeleteSkill)
	// §13.4 S4：启停（overlay：写回定义文件的 enabled + 立刻重校物化，禁用即撤回已物化副本）。
	v1.PATCH("/skills/:id", ctr.SetSkillEnabled)
	// §13.4 S2：物化体检（只读）：期望摘要 vs 实测摘要 + status，回答"agent 实际读到的与注册表一致吗"。
	v1.GET("/skills/:id/materialization", ctr.SkillMaterialization)
	v1.GET("/mcps", ctr.ListMcpServers)
	// M2 写面（MCP 侧）：闸门比 skill 多（transport 枚举 / stdio 绝对路径 / 密钥引用词法 /
	// 明文与密钥键不重复），写面一个都不新开也不放行——定义怎么进注册表就走同一条路。
	// DELETE 会一并摘除该 server 归属的工具条目（否则 /tools 里留着一个调不通的能力）。
	v1.POST("/mcps", ctr.CreateMcpServer)
	v1.PUT("/mcps/:id", ctr.UpdateMcpServer)
	v1.DELETE("/mcps/:id", ctr.DeleteMcpServer)
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
