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
	v1.GET("/sessions", h.Session.List)
	v1.POST("/sessions", h.Session.Create)
	v1.GET("/sessions/:id", h.Session.Get)
	v1.PATCH("/sessions/:id", h.Session.Update)
	v1.GET("/sessions/:id/history", h.Session.History)
	v1.POST("/sessions/:id/prompt", h.Session.Prompt)
	v1.POST("/sessions/:id/cancel", h.Session.Cancel)
	v1.GET("/sessions/:id/permissions", h.Session.Permissions)
	v1.POST("/sessions/:id/permission", h.Session.ResolvePermission)
	v1.GET("/sessions/:id/permissions/always", h.Session.AlwaysAllowed)
	v1.POST("/sessions/:id/permissions/always/revoke", h.Session.RevokeAlways)
	v1.GET("/runtimes", h.Runtime.List)
	v1.GET("/ws/events", h.WS.Handle)

	MountWeb(r)
	return r
}

// health 健康检查。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
