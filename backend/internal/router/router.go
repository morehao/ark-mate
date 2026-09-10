// Package router 负责 gin.Engine 初始化与路由注册（控制面 HTTP 入口）。
//
// 装配范式（对齐 ark-iam）：本包是**唯一的传输层装配点**——
//   - Ports 声明路由需要哪些领域端口，由上层（internal/app）注入实现；
//   - 每个领域一个 routes_*.go，各自构造控制器并注册自己的路由；
//   - 鉴权等横切关注点经 internal/middleware 挂载。
//
// 路由前缀由 golib ginserver 的版本分组给出：/{version}/{appName}（v1/forge）。
// 对外契约以本包注册的路径为准；README「控制面接口摘要」由
// TestReadmeRoutesMatchRouter 守卫，新增/改名路由必须同步。
package router

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/middleware"
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/internal/service/svccapability"
	"github.com/morehao/ark-forge/internal/service/svcconversation"
	"github.com/morehao/ark-forge/internal/service/svcerr"
	"github.com/morehao/ark-forge/internal/service/svcexport"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/ark-forge/internal/service/svcsearch"
	"github.com/morehao/ark-forge/internal/service/svcusage"
	"github.com/morehao/ark-forge/internal/service/svcworkspace"
	"github.com/morehao/golib/biz/gserver/ginserver"
)

// AppName 控制面应用的业务标识，构成路由前缀部分。
const AppName = "forge"

// Ports 汇总路由注册所需的领域端口。
//
// 与"一个汇总大接口"的区别：这里只是装配点把实现分发给各控制器时的中转结构，
// 控制器自身仍只持有自己那一片窄接口（见 internal/controller/ctr*）。
type Ports struct {
	Agent        svcagent.AgentSvc
	Conversation svcconversation.ConversationSvc
	Workspace    svcworkspace.WorkspaceSvc
	Runtime      svcruntime.RuntimeSvc
	Capability   svccapability.CapabilitySvc
	Usage        svcusage.UsageSvc
	Search       svcsearch.SearchSvc
	Export       svcexport.ExportSvc
	// ErrMapper 领域错误 → 传输层错误码（P5 收敛错误码后删除）。
	ErrMapper svcerr.Mapper
}

// Options 控制面装配选项（A6：单操作员 token）。
type Options struct {
	// Token 非空时，除 /health 外的所有 /v1/forge 路由都要求 Bearer token：
	// REST 用 `Authorization: Bearer <token>`；WS 因浏览器无法设握手头，额外接受 `?token=<token>`。
	// 空 = 不鉴权（默认，仅绑定回环地址时的历史行为）。
	Token string
}

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
//
// bus 是传输层设施（WS 通道的订阅源）而非领域端口，故与 Ports 并列传入。
// 实时通道契约见 docs/design-and-roadmap.md §4.5；A6 起可选单操作员 token 鉴权。
func New(ports Ports, bus *eventbus.Bus, opts ...Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	// golib v1.32.14 起 versions 由可变参数改为切片，并默认挂载 ginmiddleware.Trace()
	// （替代旧的 otelgin，未接入 otel 时仅透传 trace id，供 glog 关联链路）。
	groups := ginserver.NewRouterGroups(r, AppName, []ginserver.VersionGroup{{
		Version: ginserver.ApiVersionV1,
	}})
	v1 := groups.MustGetGroup(ginserver.ApiVersionV1)

	// 健康检查保持免鉴权（容器编排/反代的 liveness 探针不携带令牌）；
	// 之后的全部路由挂鉴权中间件（含 WS）。
	v1.GET("/health", health)
	if len(opts) > 0 && strings.TrimSpace(opts[0].Token) != "" {
		v1.Use(middleware.Token(opts[0].Token))
	}

	registerAgent(v1, ports)
	registerWorkspace(v1, ports)
	registerConversation(v1, ports)
	registerRuntime(v1, ports)
	registerCapability(v1, ports)
	registerObservability(v1, ports, bus)

	MountWeb(r)
	return r
}

// health 健康检查（唯一免鉴权端点：不依赖任何领域端口）。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
