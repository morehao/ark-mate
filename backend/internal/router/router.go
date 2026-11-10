// Package router 负责 gin.Engine 初始化与路由注册（控制面 HTTP 入口）。
//
// 装配范式（v3.1 §3.2.1 规则 4）：本包只做**engine + 中间件 + 路由表**。
//   - 控制器一律由 `internal/bootstrap/assembly` 构造后经 `Deps.Controllers` 交进来；
//     本包**不构造用例、不 import `internal/service/*`、`internal/persistence/store`、`internal/domain`**；
//   - 每个领域一个 routes_*.go，只注册自己的路由；
//   - 鉴权等横切关注点经 internal/middleware 挂载。
//
// 历史上的 `router.Ports`（"依赖事实杂物袋"）已删除：它是"装配分散在三处"的中间形态，
// v3.1 的装配根是 `internal/bootstrap/assembly`，路由层退化为纯交付。
//
// 路由前缀由 golib ginserver 的版本分组给出：/{version}/{appName}（v1/mate）。
// 对外契约以本包注册的路径为准；README「控制面接口摘要」由
// TestReadmeRoutesMatchRouter 守卫，新增/改名路由必须同步。
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrapp"
	"github.com/morehao/ark-mate/internal/controller/ctrartifact"
	"github.com/morehao/ark-mate/internal/controller/ctrattachment"
	"github.com/morehao/ark-mate/internal/controller/ctrautomation"
	"github.com/morehao/ark-mate/internal/controller/ctrcapability"
	"github.com/morehao/ark-mate/internal/controller/ctrexport"
	"github.com/morehao/ark-mate/internal/controller/ctrlibrary"
	"github.com/morehao/ark-mate/internal/controller/ctrmaterial"
	"github.com/morehao/ark-mate/internal/controller/ctrmodel"
	"github.com/morehao/ark-mate/internal/controller/ctrruntime"
	"github.com/morehao/ark-mate/internal/controller/ctrsearch"
	"github.com/morehao/ark-mate/internal/controller/ctrsession"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/controller/ctrusage"
	"github.com/morehao/ark-mate/internal/controller/ctrworkspace"
	"github.com/morehao/ark-mate/internal/controller/ctrws"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/golib/biz/gserver/ginserver"
	"github.com/morehao/golib/biz/gserver/ginupload"
	"github.com/morehao/golib/filestore"
)

// AppName 控制面应用的业务标识，构成路由前缀部分。
const AppName = "mate"

// Controllers 已构造的控制器集合（装配根的产物，路由层只消费）。
//
// 每个字段都是某一领域控制器的**接口**（`ctr<领域>` 包内声明）；路由层不认识任何实现。
type Controllers struct {
	Session    ctrsession.SessionCtr
	Search     ctrsearch.SearchCtr
	Export     ctrexport.ExportCtr
	Workspace  ctrworkspace.WorkspaceCtr
	Runtime    ctrruntime.RuntimeCtr
	Model      ctrmodel.ModelCtr
	Provider   ctrmodel.ProviderCtr
	Capability ctrcapability.CapabilityCtr
	// App 应用定义面（一期 A+Slot，`/apps` 六接口）。
	App        ctrapp.AppCtr
	Automation ctrautomation.AutomationCtr
	Library    ctrlibrary.LibraryCtr
	Usage      ctrusage.UsageCtr
	System     ctrsystem.SystemCtr
	WS         ctrws.WSCtr
	// Attachment 附件面上传/回读/预览（v32）。
	Attachment ctrattachment.AttachmentCtr
	// Material 资料库目录树 + 上传 + 关键词检索（v33）。
	Material ctrmaterial.MaterialCtr
	// Artifact 产物溯源（v34）。
	Artifact ctrartifact.ArtifactCtr
}

// Deps 路由装配依赖（只有已构造的控制器与交付层设施）。
type Deps struct {
	Controllers Controllers

	// Web 已解析的前端资产（装配层用 ResolveAssets 得到）。零值 = 不挂前端（只有测试会这样）。
	Web AssetInfo

	// AssetWatch 开发期资产重建通知（P6-A）。**零值（nil）= 关闭**——交付形态（桌面壳/容器/
	// 自建二进制）不传，于是 `/dev/assets/events` 这条路由根本不存在，该路径走 NoRoute 的
	// SPA 回落（资产层的既有语义）。装配层只在 `MATE_ASSET_WATCH`/`--asset-watch` 显式给了
	// 正时长时才构造它（见 internal/bootstrap/assembly）。
	//
	// 为什么把"开关"做成一个对象而不是 bool：对象非 nil 就是开着，注册点因此可以写成
	// "有值才挂"，不需要再加一个字段去描述"开着但没东西可挂"。
	AssetWatch *AssetWatcher

	// Origin 回环监听面的 Host 白名单 + Origin 矩阵（M3-5，internal/middleware）。
	//
	// nil / 未启用 = 不判定（测试路径与**非回环监听**：设计 §9.4 明确 Host/Origin 白名单属于
	// "对外产品化缺的四件事"之一，本期只在回环监听面生效，由装配层打 WARN 说清）。
	// 非 nil 时它同时供给两处，且**必须是同一个对象**：v1 组的 REST/`/health` 中间件，
	// 与 WS 控制器的握手判定（见 registerStream）。
	Origin *middleware.OriginPolicy

	// FileStore 是 golib filestore 实例，供 `ginupload.Register` 挂**全局唯一的上传面** `/files`
	// （产物溯源 P0′：收字节收口，见设计 §3.4）。nil = 不挂（memory 模式无持久层）。
	//
	// 与 attachmentstore.Store.FileStore() 是**同一个实例**：上传、去重、附件索引共用一套字节底座。
	FileStore *filestore.FileStore
}

// New 构建 gin.Engine 并注册控制面 REST + WS 实时事件路由。
//
// 只做交付：所有控制器都已构造好，本函数不调任何 `New*` 用例构造函数。
// 实时通道契约见 docs/architecture.md §6.2。
func New(d Deps) *gin.Engine {
	ctrls := d.Controllers

	r := gin.New()
	// **不要打开 `r.ContextWithFallback`**（2026-10-27 实测，见
	// `.dsh/docs/plans/2026-10-27-context-boundary-plan.md` §0）。
	//
	// 打开它会让 `c` 看起来可以当 `context.Context` 直接传给下游，但 `*gin.Context` 是
	// 池化复用对象：`database/sql` 的 `Rows.initContextClose` 见 `ctx.Done() != nil` 就起一个
	// goroutine 长期监听，请求结束、`reset()` + `pool.Put(c)`、下一个请求写 `c.Request = req`
	// 之后，那条 goroutine 仍在读同一个对象 —— `-race` 下稳定报 DATA RACE。
	// 因此 handler 一律传 `c.Request.Context()`，它是纯 ctx，可被安全的长期持有。
	r.ForwardedByClientIP = true
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
	// 为什么不让中间件也盖住 WS：WS 的入站判定归控制器（在其 Accept 之前完成），来源判定
	// 与 REST 共用同一个对象——两处都挂会让 WS 的那份判定变成
	// 永不执行的摆设，且"哪一层真正拦住了 WS"变得含糊。两处共用**同一个** OriginPolicy 对象
	// （Decide 是唯一实现），所以哪一种挂法都不会分叉。
	//
	// 不在组里的是静态资产与 SPA 回落（`/`、`/assets/*`、前端深链）：页面导航与子资源加载
	// 不带 Origin，把 Host 白名单压到它们头上会让"打开面板"这件事本身变成 421——真正的攻击面
	// 是 API，页面本身没有任何凭据可偷（设计 §7.2 的凭据分层）。
	wsGroup := v1.Group("")
	v1.Use(middleware.Origin(d.Origin))

	// 健康检查（容器编排/反代的 liveness 探针）。
	v1.GET("/health", health)
	// 实时事件流：来源判定走**同一个** OriginPolicy（WS 也要看 Host），见 ctrws.Options.OriginCheck。
	registerStream(wsGroup, ctrls.WS, d.Origin)

	// 其余全部 REST 路由。
	rest := v1.Group("")

	registerWorkspace(rest, ctrls.Workspace)
	registerAttachment(rest, ctrls.Attachment)
	registerSession(rest, ctrls.Session, ctrls.Search, ctrls.Export)
	registerRuntime(rest, ctrls.Provider, ctrls.Model, ctrls.Runtime)
	registerCapability(rest, ctrls.Capability)
	registerApps(rest, ctrls.App)
	registerAutomation(rest, ctrls.Automation)
	registerLibrary(rest, ctrls.Library)
	registerMaterial(rest, ctrls.Material)
	registerArtifact(rest, ctrls.Artifact)
	registerUsage(rest, ctrls.Usage)
	registerSystem(rest, ctrls.System)

	// 全局唯一的上传面（产物溯源 P0′）：golib ginupload 一步挂上整套 `/files` 资源
	// （上传 / 分片 / 详情 / 删除 / presign / serve）。业务模块只建索引、不再各自收字节。
	// nil（memory 模式无持久层）→ 不挂，上传面整体不可用（与附件面 40501 同源）。
	if d.FileStore != nil {
		ginupload.Register(rest, d.FileStore)
	}

	// presign 误路由守卫：签名字节通路只挂在核心 origin（`/v1/mate/files/:id/presign-url`）。前端预览
	// 若把请求打到别的宿主，路径会退化成 `/v1/files/...` / `/files/...`（未命中任何路由）——这里把它
	// 从误导人的 404 改成明确的 405 + 信封，指向"改用核心 origin"。挂在注册之后、引擎级（见实现注释：
	// v1 组中间件跑不到 NoRoute，只有引擎级 Use 能进 NoRoute 链；已注册路由的链在注册时已快照，
	// 不含守卫，命中请求零改动）。
	guardPresignMisroute(r, rest)

	MountWeb(r, d.Web)
	// 开发期资产重建通知（P6-A）：**开关与注册点必须同处**（archtest 有静态守卫钉这条）——
	// 分开放就会出现"开关忘了接"的形态：配置写着开了，路由却没挂，页面永远不刷新且无人报错。
	if d.AssetWatch != nil {
		d.AssetWatch.Mount(r)
	}
	return r
}

// health 健康检查（唯一免鉴权端点：不依赖任何领域端口）。
func health(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}
