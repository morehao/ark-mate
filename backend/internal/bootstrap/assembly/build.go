package assembly

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/golib/glog"
)

// ——— v3.1 §3.4：唯一装配根 ———
//
// `Build` 做两件事，且**只有它**做：
//  1. 构造对象图（事件总线 / 执行面 / 领域平面 / 会话编排 / 用例 / 控制器 / 路由）；
//  2. 按 `registry.Sequence` 的声明顺序执行启动动作（导入、恢复、定义装载、监听绑定…），
//     失败时按逆序回滚已启动的步骤。
//
// 它**不启动 HTTP、不读 os.Args、不处理信号**：那些是 `internal/bootstrap/app`（应用壳）的事。
// 这是 `pkg` 之外"装配边单向性"的落点——除本包外，没有任何包同时 import service 与 store/controller。

// Options 装配可选项（M1-1/M1-2；值由命令层解析后交来）。
type Options struct {
	// Payloads 桌面壳**已解析**的随包载荷清单（M3-4）：核心只把它透出到 `/meta`，不自己探测。
	Payloads []dtosystem.PayloadInfo
}

// Option 装配可选项的函数式写法。
type Option func(*Options)

// WithPayloads 注入随包载荷清单。
func WithPayloads(payloads []dtosystem.PayloadInfo) Option {
	return func(o *Options) { o.Payloads = payloads }
}

// BuildResult 一次装配的产物（应用壳与命令树只消费它）。
//
// 字段全是**已构造/已就绪**的对象；Handler/Listener 在装配成功后
// 一定非 nil（HTTP 服务由 `internal/bootstrap/app` 用这两者构造，装配根不持有 `http.Server`）。
//
// `Handler` 是 `http.Handler` 而不是 `*gin.Engine`：装配根可以知道具体适配器，但**不必**把
// 框架类型写进自己的产物类型——应用壳与命令树只消费 `Handler`，Web 框架止步于入站适配层。
type BuildResult struct {
	Planes       *registry.Planes
	Runtime      *runtime.Registry
	Execution    *runtimeconn.Plane
	Session      *sessionruntime.Service
	Orchestrator *sessionruntime.Orchestrator
	Bus          *eventbus.Bus

	Services    Services
	Controllers router.Controllers
	Handler     http.Handler
	Listener    net.Listener
	Assets      router.AssetInfo

	closeOnce sync.Once
	stop      func()
}

// Close 按启动的逆序关闭本次装配（HTTP 服务请先由应用壳停掉再调用）。
func (r *BuildResult) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.stop != nil {
			r.stop()
		}
	})
}

// Build 装配整个后端并执行启动序列。
func Build(ctx context.Context, cfg config.Config, sources config.Sources, opts ...Option) (*BuildResult, error) {
	o := Options{}
	for _, fn := range opts {
		fn(&o)
	}

	// 产品装配：**只注册构建期选中的那一个底座**（见 internal/runtime/build.go）。
	// 远程 runtime（A5）：runtimes.json 声明的远程 ACP 端点参与同一次选择——选中的 id 命中其中
	// 一条声明时实现来自远端；未选中的声明不进入本进程的注册表（它们是**候选**，不是装配结果）。
	// 与内置 id 冲突、或选中的 id 谁都不是 → 启动即失败（不静默覆盖内置，也不给出空注册表）。
	remoteSpecs := make([]runtime.Spec, 0, len(cfg.RemoteRuntimes))
	for _, rt := range cfg.RemoteRuntimes {
		remoteSpecs = append(remoteSpecs, runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
	}
	reg, err := runtime.ProductRegistry(AllAdapters(), remoteSpecs)
	if err != nil {
		return nil, usageErr("serve: %v", err)
	}
	glog.Infow(ctx, "serve: 已装配底座", "runtime", runtime.SelectedRuntime(),
		"remotes", len(cfg.RemoteRuntimes))

	// runtime 子进程的配置隔离（M3-3；设计 §6.6）：把各底座的 home / cache / npm prefix 重定向到
	// 数据目录下的专属子目录，避免平台拉起的底座去读写用户自己装的那份配置（也避免被它改写）。
	// 只作用于**本地子进程** runtime（远程端点不拉进程，Launch.Env 不会被读），已有键不覆盖——
	// 适配器或 provider 档案显式给的取值优先（见 runtime.Registry.ApplyIsolationEnv）。
	if ids := reg.ApplyIsolationEnv(cfg.HomeDir); len(ids) > 0 {
		// 日志是**用户面**（界面里就有日志页），所以数据目录按显示名记（v19，`~/.ark-mate`）：
		// 绝对机器路径会随截图/日志导出外流，而 `~` 一样能定位。机器消费面（`/meta`、`--json`）
		// 保留绝对路径——那两条是"事实"，不是给人看的叙述。
		glog.Infow(ctx, "serve: 已注入 runtime 配置隔离环境", "home", config.DisplayHome(cfg.HomeDir), "runtimes", ids)
	}

	// 拉底座前的统一准备：落盘 mate-acp profile + 注入 office 随包 env（详见 prepare_runtime.go）。
	// 抽成公共函数是因为 `mate demo` 等控制面命令也拉底座，必须在**每一处**拉底座前都跑。
	// 返回值是 answerer 令牌：同一份值既注入 dsh env、又交给路由层做 `/elicitations/ask` 强校验。
	answererToken := PrepareRuntimeEnv(ctx, reg, cfg.HomeDir, "http://"+cfg.Addr)

	as := &assembly{
		cfg:           cfg,
		sources:       sources,
		opts:          o,
		mode:          cfg.ResolvedStoreMode(),
		reg:           reg,
		bus:           eventbus.New(),
		answererToken: answererToken,
	}
	// 执行面由**装配层**造，同一个实例同时交给 Service（记账）与 API 面（读面）——
	// 两个平面各自记账会让"诊断说 stopped、连接却活着"立刻发生。
	as.rconn = runtimeconn.NewPlaneFor(reg)
	// 领域平面由 registry 造**一次**，核心与网关共用同一套。
	as.planes = registry.NewPlanes(reg)
	as.svc = sessionruntime.NewService(reg, as.bus,
		sessionruntime.WithPlanes(as.planes), sessionruntime.WithRuntimePlane(as.rconn))

	as.lifecycle = registry.New(as.steps()...)
	if err := as.lifecycle.Start(ctx); err != nil {
		return nil, err
	}

	res := &BuildResult{
		Planes:       as.planes,
		Runtime:      as.reg,
		Execution:    as.rconn,
		Session:      as.svc,
		Orchestrator: as.orch,
		Bus:          as.bus,
		Services:     as.services,
		Controllers:  as.controllers,
		Handler:      as.engine,
		Listener:     as.ln,
		Assets:       as.assets,
	}
	res.stop = func() { as.lifecycle.Stop(context.Background()) }
	return res, nil
}
