// Package router 负责 gin.Engine 初始化与路由注册。
// 基于 golib ginserver 的版本分组抽象（/{version}/{appName}），
// 自动挂载 otelgin + AccessLog 等默认中间件。
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/biz/gserver/ginserver"
)

// AppName 控制面应用的业务标识，构成路由前缀部分。
const AppName = "forge"

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
// 实时通道契约见 docs/design-and-roadmap.md §4.5（M0 无鉴权）。
func New(h *handler.Handler) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	groups := ginserver.NewRouterGroups(r, AppName, ginserver.VersionGroup{
		Version: ginserver.ApiVersionV1,
	})
	v1 := groups.MustGetGroup(ginserver.ApiVersionV1)

	v1.GET("/health", health)
	v1.GET("/agents", h.Agent.List)
	// P2 Agent 管理面：注册向导（definitions 持久化）+ 启停 + 热更新 + 删除。
	v1.POST("/agents", h.Agent.Create)
	v1.PATCH("/agents/:id", h.Agent.Update)
	v1.DELETE("/agents/:id", h.Agent.Delete)
	v1.POST("/agents/:id/start", h.Agent.Start)
	v1.POST("/agents/:id/stop", h.Agent.Stop)
	// P2 §8.1 统一能力目录：某 Agent 可见的 skill/MCP/tool（按候选 runtime 聚合，只读）。
	v1.GET("/agents/:id/capabilities", h.Capability.AgentCapabilities)
	v1.GET("/conversations", h.Conversation.List)
	v1.GET("/conversations/search", h.Conversation.Search)
	v1.POST("/conversations", h.Conversation.Create)
	v1.GET("/conversations/:id", h.Conversation.Get)
	v1.PATCH("/conversations/:id", h.Conversation.Update)
	v1.GET("/conversations/:id/history", h.Conversation.History)
	v1.GET("/conversations/:id/runs", h.Conversation.Runs)
	v1.POST("/conversations/:id/prompt", h.Conversation.Prompt)
	v1.POST("/conversations/:id/cancel", h.Conversation.Cancel)
	v1.GET("/conversations/:id/permissions", h.Conversation.Permissions)
	v1.POST("/conversations/:id/permission", h.Conversation.ResolvePermission)
	// 会话级权限记忆（allow/reject 双侧，P0 起对称语义）。
	v1.GET("/conversations/:id/permissions/memories", h.Conversation.PermissionMemories)
	v1.POST("/conversations/:id/permissions/memories/revoke", h.Conversation.RevokeMemory)
	// 兼容别名：旧路径 /permissions/always（历史客户端/脚本）；新路径语义一致。
	v1.GET("/conversations/:id/permissions/always", h.Conversation.PermissionMemories)
	v1.POST("/conversations/:id/permissions/always/revoke", h.Conversation.RevokeMemory)
	v1.GET("/conversations/:id/elicitations", h.Conversation.Elicitations)
	v1.POST("/conversations/:id/elicitation", h.Conversation.ResolveElicitation)
	v1.GET("/runtimes", h.Runtime.List)
	// P2 provider 注入面：密钥只写不读回。
	v1.GET("/providers", h.Provider.List)
	v1.PATCH("/providers/:id", h.Provider.Update)
	// P1 能力数据面：Skill/MCP/Tool 目录（platform 唯一事实源，REST 列出能力）。
	v1.GET("/skills", h.Capability.ListSkills)
	v1.GET("/mcps", h.Capability.ListMcpServers)
	v1.GET("/tools", h.Capability.ListTools)
	// P2 §5.2 MCP live 探测：tools/list → 写 Tool 目录（显式触发；不做隐式启动探测，行为不回归）。
	v1.POST("/mcps/:id/probe", h.Capability.ProbeMcpServer)
	// M3 首项 用量统计：runs 计数/时长 + 会话上下文/费用快照聚合（只读）。
	v1.GET("/usage/summary", h.Usage.Summary)
	v1.GET("/ws/events", h.WS.Handle)

	MountWeb(r)
	return r
}

// health 健康检查。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
