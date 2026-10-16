// Package router 负责 gin.Engine 初始化与路由注册（控制面 HTTP 入口）。
//
// 装配范式（对齐 ark-iam）：本包是**唯一的传输层装配点**——
//   - Ports 声明路由需要哪些领域端口，由上层（internal/app）注入实现；
//   - 每个领域一个 routes_*.go，各自构造控制器并注册自己的路由；
//   - 鉴权等横切关注点经 internal/middleware 挂载。
//
// 路由前缀由 golib ginserver 的版本分组给出：/{version}/{appName}（v1/mate）。
// 对外契约以本包注册的路径为准；README「控制面接口摘要」由
// TestReadmeRoutesMatchRouter 守卫，新增/改名路由必须同步。
package router

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/internal/service/svcmodel"
	"github.com/morehao/ark-mate/internal/service/svcprovider"
	"github.com/morehao/ark-mate/internal/service/svcsession"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/biz/gserver/ginserver"
)

// AppName 控制面应用的业务标识，构成路由前缀部分。
const AppName = "mate"

// Ports 汇总路由注册所需的领域端口。
//
// 与"一个汇总大接口"的区别：这里只是装配点把实现分发给各控制器时的中转结构，
// 控制器自身仍只持有自己那一片窄接口（见 internal/controller/ctr*）。
//
// **最终形状：每一格都是"依赖事实"**（域平面 / 执行面 / 注册表 / 库句柄 / 配置读取口），
// 用例实例一律由控制器在自己的构造函数里造（`svcruntime.New` / `svcsystem.New` /
// `svclibrary.NewLibrary` / `svcautomation.New`…）。本包**不 import `internal/service/svc*`**：
// 依赖链是 `router → controller → service`，路由装配只该认识控制器。
// 这条由 `internal/archtest/svc_layer_consumer_test.go` 钉住——它的失效是静默的
// （多加一行 svc import 也能编译，只是把一条本该单向的边变成双向）。
//
// **过渡期**：仍有几格是 `service/svc*` 的类型（`Session`/`ProviderCatalog`/`Model`/`ModelHost`）
// ——它们各自的实现还在 `manager`，控制器暂时造不出来。每迁完一个域就少一格
// （已消失的：`Library`/`Runtime`/`System`/`Automation`/`Capability`，见 P4a 正文各刀的记录），
// 全部迁完时这几行会**自己消失**（白名单 `transitionalServiceImporters` 也是同一张进度表）。
// 别在过渡期往里加新的 svc 格：那正是 2026-10-16 被判定为"骚操作"的那批别名的来路。
type Ports struct {
	Session svcsession.SessionSvc
	// InteractionSource 会话交互面的三份只读事实（挂起权限 / 权限记忆 / 挂起提问）。
	//
	// 那份状态归**执行面**持有（`Orchestrator` 私有字段），而详情读面用例（`svcsession.NewDetail`）
	// 在 ctrsession 里造——所以装配点交的是这三条**窄端口**，不是执行面本身（P4a 正文第四十刀）。
	InteractionSource svcsession.InteractionSource
	// Runtime 底座**执行面本身**（`*runtimeconn.Plane`；P4a 正文第七刀）——`app` 交的是依赖事实，
	// 底座面的用例（`svcruntime.New`）由 `ctrruntime` 在自己的构造函数里造。
	Runtime *runtimeconn.Plane
	// ProviderPlane 供应商域平面（**依赖事实**：域的类型，不是 `service/*` 的类型）；
	// ProviderCatalog 是自检要的模型目录读面。两者一起交给 `ctrprovider.NewProviderCtr`，
	// 档案用例与自检用例**都在控制器里造**（P4a 正文第四刀）。
	ProviderPlane   *provider.Plane
	ProviderCatalog svcprovider.Catalog
	// Store 持久层句柄（**依赖事实**：memory 模式为 nil）；模型清单的写面用它落库。
	Store *store.Store
	// Model 还没搬完的那半边模型面（读清单 / 运行时目录 / 同步），过渡期由 manager 满足。
	Model svcmodel.ModelSvc
	// ModelHost 写面之外的模型面（读一条的投影 / 生效面回执 / 同步触发），过渡期端口；
	// 与 Store、ProviderPlane 一起交给 `ctrmodel.NewModelCtr`，写面用例在控制器里造。
	ModelHost       svcmodel.Host
	AutomationPlane *automation.Plane
	// Library 读模型的事实来源（P4a 正文第六刀）：三张能力表 + 技能投影 + 运行期事实。
	// 用例实例（`svclibrary.NewLibrary`）由 ctrlibrary 自己造——`app`/`router` 都不 import svc。
	Capabilities *capability.Registry
	SkillPlane   *skill.Plane
	Registry     *runtime.Registry
	// WorkspacePlane 工作空间域平面（登记表读模型等事实的来源）。
	WorkspacePlane *workspace.Plane
	// Picker 宿主目录选择器平面（判定/配方/spawn 全在领域里；控制器的窄端口消费它）。
	Picker *workspace.Picker
	// McpPlane MCP 注册表/探测态平面（连接器目录读模型的事实来源之一）。
	McpPlane   *mcp.Plane
	McpSecrets *mcp.Secrets
	// McpGatewayEnabled 平台 MCP 网关开关（**运行期可变**，所以是读取口而不是快照值）。
	McpGatewayEnabled func() bool
	// SessionPlane 会话域平面 + Bus 事件总线（**依赖事实**）：导出用例与它的事实来源都由
	// ctrexport 在自己的构造函数里造（P4a 正文第八刀）；`Store` 那一格复用上面模型面的句柄。
	SessionPlane *session.Plane
	Bus          *eventbus.Bus
}

// CapabilityHostFacts 报告"宿主能不能抬起系统目录面板"（`native-dialog` 的可用性判据）。
//
// 定义在 router 而不是某个 svc 端口上：它是**装配期的事实查询**（一个纯读函数），
// 与领域端口不是一类东西；实现方是 manager（判据见 manager/workspace_picker.go）。
type CapabilityHostFacts func() bool

// Options 控制面装配选项（M1-1：凭据表）。
type Options struct {
	// HostFacts 可选：宿主能力事实（nil = 未知，相关能力一律报 false）。
	HostFacts CapabilityHostFacts

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

	// Meta 系统面（`/v1/mate/meta`）里由**装配层**提供的部分（核心版本、数据目录、随包载荷…）；
	// 资产相关字段由路由层按**已解析的资产**补全（M2-1 起不看构建 tag，M2-3 接真值）。
	// nil = 用默认值（版本 + 空载荷）。
	Meta func() ctrsystem.MetaSource

	// Web 已解析的前端资产（装配层用 ResolveAssets 得到）。零值 = 不挂前端（只有测试会这样）。
	Web AssetInfo

	// AssetWatch 开发期资产重建通知（P6-A）。**零值（nil）= 关闭**——交付形态（桌面壳/容器/
	// 自建二进制）不传，于是 `/dev/assets/events` 这条路由根本不存在，该路径走 NoRoute 的
	// SPA 回落（资产层的既有语义）。装配层只在 `MATE_ASSET_WATCH`/`--asset-watch` 显式给了
	// 正时长时才构造它（见 internal/app）。
	//
	// 为什么把"开关"做成一个对象而不是 bool：对象非 nil 就是开着，注册点因此可以写成
	// "有值才挂"（见下方 MountWeb 之后那段），不需要再加一个字段去描述"开着但没东西可挂"。
	AssetWatch *AssetWatcher

	// Origin 回环监听面的 Host 白名单 + Origin 矩阵（M3-5，internal/middleware）。
	//
	// nil / 未启用 = 不判定（测试路径与**非回环监听**：设计 §9.4 明确 Host/Origin 白名单属于
	// "对外产品化缺的四件事"之一，本期只在回环监听面生效，由装配层打 WARN 说清）。
	// 非 nil 时它同时供给两处，且**必须是同一个对象**：v1 组的 REST/`/health` 中间件，
	// 与 WS 控制器的握手判定（见 registerStream）。
	Origin *middleware.OriginPolicy
}

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
//
// bus 是传输层设施（WS 通道的订阅源）而非领域端口，故与 Ports 并列传入。
// 实时通道契约见 docs/architecture.md §6.2。
func New(ports Ports, bus *eventbus.Bus, opts ...Options) *gin.Engine {
	o := opts0(opts)
	creds := o.Credentials

	r := gin.New()
	r.Use(gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	// golib v1.32.14 起 versions 由可变参数改为切片，并默认挂载 ginmiddleware.Trace()
	// （替代旧的 otelgin，未接入 otel 时仅透传 trace id，供 glog 关联链路）。
	groups := ginserver.NewRouterGroups(r, AppName, []ginserver.VersionGroup{{
		Version: ginserver.ApiVersionV1,
	}})
	v1 := groups.MustGetGroup(ginserver.ApiVersionV1)

	// Host 白名单 + Origin 矩阵（M3-5）挂在 **v1 整组**上：REST 与 `/health` 都属于"有人拿浏览器/
	// 脚本打进来的 API 面"，一律先过这道判定（含 /health：DNS rebinding 只要能碰到任意一条路径，
	// 就说明 Host 白名单没生效）。必须在注册路由**之前** Use——gin 在注册时就冻结 handler 链
	// （见 internal/controller/ctrws/ws_test.go 的同款注记）。
	//
	// **WS 单独处置**：事件流那一组来源判定在控制器里做（Accept 之前，见 ctrws.Options.OriginCheck），
	// 所以它的组要在 Use 之前取——gin 的 Group() 会复制当时的 handler 链。
	// 为什么不让中间件也盖住 WS：事件流的凭据本来就走**带内首帧**、不挂 REST 凭据中间件
	// （浏览器无法为 WS 握手设请求头），来源判定同理归控制器——两处都挂会让 WS 的那份判定变成
	// 永不执行的摆设，且"哪一层真正拦住了 WS"变得含糊。两处共用**同一个** OriginPolicy 对象
	// （Decide 是唯一实现），所以哪一种挂法都不会分叉。
	//
	// 不在组里的是静态资产与 SPA 回落（`/`、`/assets/*`、前端深链）：页面导航与子资源加载
	// 不带 Origin，把 Host 白名单压到它们头上会让"打开面板"这件事本身变成 421——真正的攻击面
	// 是 API，页面本身没有任何凭据可偷（设计 §7.2 的凭据分层）。
	wsGroup := v1.Group("")
	v1.Use(middleware.Origin(o.Origin))

	// 健康检查保持免鉴权（容器编排/反代的 liveness 探针不携带令牌）。
	v1.GET("/health", health)
	// 实时事件流：鉴权在**带内**（首帧 attach 带 stream 面凭据），因此不挂 REST 凭据中间件。
	// 来源判定走**同一个** OriginPolicy（WS 也要看 Host），见 ctrws.Options.OriginCheck。
	registerStream(wsGroup, bus, creds, o.Origin)

	// 其余全部 REST 路由统一要求 rest 面凭据（空表时中间件是放行 no-op）。
	rest := v1.Group("")
	rest.Use(middleware.Credential(creds, credential.ScopeREST))

	registerWorkspace(rest, ports)
	registerSession(rest, ports)
	registerRuntime(rest, ports)
	registerCapability(rest, ports)
	registerAutomation(rest, ports)
	registerLibrary(rest, ports)
	registerUsage(rest, ports)
	meta := o.Meta
	if meta == nil {
		meta = defaultMeta
	}
	registerSystem(rest, meta, o.Web, o.HostFacts, func() dtoruntime.IdentityResp {
		if ports.Runtime == nil {
			return dtoruntime.IdentityResp{}
		}
		// 这里取的是**执行面**的 `Identity`（P4a：`ports.Runtime` 交的是 `*runtimeconn.Plane`）：
		// 底座身份本来就是它的读面，用例层那一层只是把名字适配成 API 的口语。
		return ports.Runtime.Identity(context.Background())
	})

	MountWeb(r, o.Web)
	// 开发期资产重建通知（P6-A）：**开关与注册点必须同处**（archtest 有静态守卫钉这条）——
	// 分开放就会出现"开关忘了接"的形态：配置写着开了，路由却没挂，页面永远不刷新且无人报错。
	if o.AssetWatch != nil {
		o.AssetWatch.Mount(r)
	}
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
