// Package router 负责 gin.Engine 初始化与路由注册。
// 基于 golib ginserver 的版本分组抽象（/{version}/{appName}），
// 自动挂载 otelgin + AccessLog 等默认中间件。
package router

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/biz/gserver/ginserver"
)

// AppName 控制面应用的业务标识，构成路由前缀部分。
const AppName = "forge"

// Options 控制面装配选项（A6：单操作员 token）。
type Options struct {
	// Token 非空时，除 /health 外的所有 /v1/forge 路由都要求 Bearer token：
	// REST 用 `Authorization: Bearer <token>`；WS 因浏览器无法设握手头，额外接受 `?token=<token>`。
	// 空 = 不鉴权（默认，仅绑定回环地址时的历史行为）。
	Token string
}

// 未授权错误码（REST 信封 code；HTTP 状态同时为 401，便于前端与反代统一处理）。
const codeUnauthorized = 40101

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
// 实时通道契约见 docs/design-and-roadmap.md §4.5；A6 起可选单操作员 token 鉴权。
func New(h *handler.Handler, opts ...Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	groups := ginserver.NewRouterGroups(r, AppName, ginserver.VersionGroup{
		Version: ginserver.ApiVersionV1,
	})
	v1 := groups.MustGetGroup(ginserver.ApiVersionV1)

	// 健康检查保持免鉴权（容器编排/反代的 liveness 探针不携带令牌）；
	// 之后的全部路由挂鉴权中间件（含 WS）。
	v1.GET("/health", health)
	if len(opts) > 0 && strings.TrimSpace(opts[0].Token) != "" {
		v1.Use(authToken(opts[0].Token))
	}
	v1.GET("/agents", h.Agent.List)
	// P2 Agent 管理面：注册向导（definitions 持久化）+ 启停 + 热更新 + 删除。
	v1.POST("/agents", h.Agent.Create)
	v1.PATCH("/agents/:id", h.Agent.Update)
	v1.DELETE("/agents/:id", h.Agent.Delete)
	v1.POST("/agents/:id/start", h.Agent.Start)
	v1.POST("/agents/:id/stop", h.Agent.Stop)
	// A3：agent 诊断快照（连接态 + stderr 诊断尾巴 + 最近失败）。
	v1.GET("/agents/:id/diagnostics", h.Agent.Diagnostics)
	// P2 §8.1 统一能力目录：某 Agent 可见的 skill/MCP/tool（按候选 runtime 聚合，只读）。
	v1.GET("/agents/:id/capabilities", h.Capability.AgentCapabilities)
	v1.GET("/conversations", h.Conversation.List)
	v1.GET("/conversations/search", h.Conversation.Search)
	// M3 ②：跨会话消息级检索（锚点 = seq）与批量导出（markdown|text|json）。
	v1.GET("/search/messages", h.Search.Messages)
	v1.POST("/conversations/export", h.Export.Conversations)
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
	// 模型目录：**运行时派生**（ACP session config options），`?agent=<id>` 指定 agent
	// （缺省取默认 agent）。平台不内置模型清单；provider 档案只影响启动 env 注入。
	v1.GET("/models", h.Model.List)
	// P1 能力数据面：Skill/MCP/Tool 目录（platform 唯一事实源，REST 列出能力）。
	v1.GET("/skills", h.Capability.ListSkills)
	v1.GET("/mcps", h.Capability.ListMcpServers)
	v1.GET("/tools", h.Capability.ListTools)
	// P2 §5.2 MCP live 探测：tools/list → 写 Tool 目录（显式触发；不做隐式启动探测，行为不回归）。
	v1.POST("/mcps/:id/probe", h.Capability.ProbeMcpServer)
	// B1：MCP 工具调用（tools/call，独立会话；工具名不含 "/"——gin 路径参数不匹配斜杠）。
	v1.POST("/mcps/:id/tools/:tool/call", h.Capability.CallMcpTool)
	// B2：MCP 资源与提示词（live 拉取，不落盘/不目录化）。
	v1.GET("/mcps/:id/resources", h.Capability.ListMcpResources)
	v1.POST("/mcps/:id/resources/read", h.Capability.ReadMcpResource)
	v1.GET("/mcps/:id/prompts", h.Capability.ListMcpPrompts)
	v1.POST("/mcps/:id/prompts/:name/get", h.Capability.GetMcpPrompt)
	// M3 首项 用量统计：runs 计数/时长 + 会话上下文/费用快照聚合（只读）。
	v1.GET("/usage/summary", h.Usage.Summary)
	v1.GET("/ws/events", h.WS.Handle)

	MountWeb(r)
	return r
}

// authToken 单操作员 token 鉴权中间件（A6）：
//   - `Authorization: Bearer <token>`（大小写不敏感、允许多余空白）；
//   - WS/EventSource 等无法设置请求头的场景接受 `?token=<token>`；
//   - 恒定时间比较（避免逐字符计时侧信道）；失败返回 HTTP 401 + REST 信封（code=40101）。
func authToken(token string) gin.HandlerFunc {
	want := []byte(token)
	return func(c *gin.Context) {
		got := ""
		if h := c.GetHeader("Authorization"); h != "" {
			if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
				got = strings.TrimSpace(h[7:])
			}
		}
		if got == "" {
			got = strings.TrimSpace(c.Query("token"))
		}
		if got != "" && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gincontext.DtoRender{
			Code:      codeUnauthorized,
			RequestID: gincontext.GetRequestID(c),
			Msg:       "未授权：缺少或无效的访问令牌（Authorization: Bearer <FORGE_TOKEN> 或 ?token=）",
			Data:      gin.H{},
		})
	}
}

// health 健康检查。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
