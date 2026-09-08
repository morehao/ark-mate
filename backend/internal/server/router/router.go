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
	v1.GET("/sessions", h.Session.List)
	v1.GET("/sessions/search", h.Session.Search)
	v1.POST("/sessions", h.Session.Create)
	v1.GET("/sessions/:id", h.Session.Get)
	v1.PATCH("/sessions/:id", h.Session.Update)
	v1.GET("/sessions/:id/history", h.Session.History)
	v1.GET("/sessions/:id/runs", h.Session.Runs)
	v1.POST("/sessions/:id/prompt", h.Session.Prompt)
	v1.POST("/sessions/:id/cancel", h.Session.Cancel)
	v1.GET("/sessions/:id/permissions", h.Session.Permissions)
	v1.POST("/sessions/:id/permission", h.Session.ResolvePermission)
	// 会话级权限记忆（allow/reject 双侧，P0 起对称语义）。
	v1.GET("/sessions/:id/permissions/memories", h.Session.PermissionMemories)
	v1.POST("/sessions/:id/permissions/memories/revoke", h.Session.RevokeMemory)
	// 兼容别名：旧路径 /permissions/always（历史客户端/脚本）；新路径语义一致。
	v1.GET("/sessions/:id/permissions/always", h.Session.PermissionMemories)
	v1.POST("/sessions/:id/permissions/always/revoke", h.Session.RevokeMemory)
	v1.GET("/sessions/:id/elicitations", h.Session.Elicitations)
	v1.POST("/sessions/:id/elicitation", h.Session.ResolveElicitation)
	v1.GET("/runtimes", h.Runtime.List)
	// P2 provider 注入面：密钥只写不读回。
	v1.GET("/providers", h.Provider.List)
	v1.PATCH("/providers/:id", h.Provider.Update)
	v1.GET("/ws/events", h.WS.Handle)

	MountWeb(r)
	return r
}

// health 健康检查。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
