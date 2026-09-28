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
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrsystem"
	"github.com/morehao/ark-forge/internal/credential"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/middleware"
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/internal/service/svcautomation"
	"github.com/morehao/ark-forge/internal/service/svccapability"
	"github.com/morehao/ark-forge/internal/service/svcconversation"
	"github.com/morehao/ark-forge/internal/service/svcexport"
	"github.com/morehao/ark-forge/internal/service/svclibrary"
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
	Automation   svcautomation.AutomationSvc
	Library      svclibrary.LibrarySvc
	Usage        svcusage.UsageSvc
	Search       svcsearch.SearchSvc
	Export       svcexport.ExportSvc
}

// Options 控制面装配选项（M1-1：凭据表）。
type Options struct {
	// Credentials 凭据表（见 internal/credential）。
	//
	// 空表 = 不鉴权（默认，仅绑定回环地址时的历史行为）。非空时：
	//   - 除 /health 外的 REST 路由要求具备 **rest 面**的凭据（`Authorization: Bearer <token>`，
	//     不接受 `?token=`）；
	//   - `/ws/events` **不挂** REST 凭据中间件——事件流的鉴权是**带内**的：
	//     连接建立后须在 5s 内发首帧 `attach` 并携带**stream 面**凭据（见 controller/ctrws）。
	//
	// 桌面态两个面互斥（restToken / streamToken），独立态是同一条令牌同时带两个面。
	Credentials *credential.Set

	// Meta 系统面（`/v1/forge/meta`）里由**装配层**提供的部分（核心版本、数据目录、随包载荷…）；
	// 资产来源字段由路由层按**已解析的资产**补全（M2-1 起不再看构建 tag）。nil = 用默认值。
	Meta func() ctrsystem.MetaInfo

	// Web 已解析的前端资产（装配层用 ResolveAssets 得到）。零值 = 不挂前端（只有测试会这样）。
	Web AssetInfo
}

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
//
// bus 是传输层设施（WS 通道的订阅源）而非领域端口，故与 Ports 并列传入。
// 实时通道契约见 docs/design-and-roadmap.md §4.5。
func New(ports Ports, bus *eventbus.Bus, opts ...Options) *gin.Engine {
	creds := opts0(opts).Credentials

	r := gin.New()
	r.Use(gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	// golib v1.32.14 起 versions 由可变参数改为切片，并默认挂载 ginmiddleware.Trace()
	// （替代旧的 otelgin，未接入 otel 时仅透传 trace id，供 glog 关联链路）。
	groups := ginserver.NewRouterGroups(r, AppName, []ginserver.VersionGroup{{
		Version: ginserver.ApiVersionV1,
	}})
	v1 := groups.MustGetGroup(ginserver.ApiVersionV1)

	// 健康检查保持免鉴权（容器编排/反代的 liveness 探针不携带令牌）。
	v1.GET("/health", health)
	// 实时事件流：鉴权在**带内**（首帧 attach 带 stream 面凭据），因此不挂 REST 凭据中间件。
	registerStream(v1, bus, creds)

	// 其余全部 REST 路由统一要求 rest 面凭据（空表时中间件是放行 no-op）。
	rest := v1.Group("")
	rest.Use(middleware.Credential(creds, credential.ScopeREST))

	registerAgent(rest, ports)
	registerWorkspace(rest, ports)
	registerConversation(rest, ports)
	registerRuntime(rest, ports)
	registerCapability(rest, ports)
	registerAutomation(rest, ports)
	registerLibrary(rest, ports)
	registerUsage(rest, ports)
	o := opts0(opts)
	meta := o.Meta
	if meta == nil {
		meta = defaultMeta
	}
	registerSystem(rest, meta, o.Web)

	MountWeb(r, o.Web)
	return r
}

// opts0 取可变选项的第一项（未传时返回零值）——本包历史上用可变参数是为了"加字段不改调用点"。
func opts0(opts []Options) Options {
	if len(opts) == 0 {
		return Options{}
	}
	return opts[0]
}

// health 健康检查（唯一免鉴权端点：不依赖任何领域端口）。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
