package assembly

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/credential"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/adapters/outbound/registry"
	"github.com/morehao/ark-mate/internal/contract/dtosystem"
	"github.com/morehao/ark-mate/internal/runtime"
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
	// Credentials 凭据表（桌面态双凭据；独立态由 MATE_TOKEN 折叠成一条，见 `credentials`）。
	Credentials *credential.Set
	// Payloads 桌面壳**已解析**的随包载荷清单（M3-4）：核心只把它透出到 `/meta`，不自己探测。
	Payloads []dtosystem.PayloadInfo
	// ConnPool 替换"会话从哪条连接来"（`session.ConnPool`；nil = 用 `runtimeconn.Pool`）。
	//
	// 生产路径构造 `Options` 时不填它。存在的唯一理由是 C2 §3.4 的验收缝：装配一个
	// **不认识协议**的纯内存连接池，控制面契约与会话事件序列必须逐字不变。
	ConnPool session.ConnPool
}

// Option 装配可选项的函数式写法。
type Option func(*Options)

// WithCredentials 注入凭据表。
func WithCredentials(creds *credential.Set) Option {
	return func(o *Options) { o.Credentials = creds }
}

// WithPayloads 注入随包载荷清单。
func WithPayloads(payloads []dtosystem.PayloadInfo) Option {
	return func(o *Options) { o.Payloads = payloads }
}

// WithConnPool 用别的连接池顶替 `runtimeconn.Pool`（见 `Options.ConnPool`）。
//
// 这是**装配缝**而不是行为开关：装配根交下来的仍是一个端口（`session.ConnPool`），
// 会话侧一行不变；换掉的只是"连接从哪来"。生产路径不传。
func WithConnPool(pool session.ConnPool) Option {
	return func(o *Options) { o.ConnPool = pool }
}

// Handler HTTP 交付面：处理器 + **已绑定的监听器**。
//
// 两者一起走：监听器是"路由在哪个端口上"的事实，处理器是"路由是什么"。应用壳只拿这两样
// 就能起 `http.Server`，不必知道装配根里的任何子系统。
type Handler struct {
	http.Handler
	Listener net.Listener
}

// Maintenance 运维面：命令树（doctor / migrate / 导入导出）的用例入口。
//
// 与 HTTP 交付面分开：它们不需要路由与监听，只需要"已构造的用例"。今天命令树仍走各自的
// 最小装配；这个端口是它们改走 `Build` 时的**唯一落点**。
type Maintenance struct {
	Services Services
}

// Probe 诊断面（应用壳只经它读状态、触发启动探测，**不直接碰平面**）。
//
// 阶段 0.5：`app` 原先直接 `res.Planes.Mcp.AutoProbe(...)`——进程壳穿透到了子系统的具体结构。
// 现在它只认这个端口；平面只作为**只读诊断**暴露给 doctor 与装配断言。
type Probe struct {
	// Meta 当前诊断面快照（等价于 `GET /meta` 的 data）。
	Meta func() dtosystem.MetaInfo
	// AutoProbeMCP 启动期 MCP 网关探测（配置开关由装配根判断，这里只执行）。
	AutoProbeMCP func(ctx context.Context) error
	// Runtime 构建期选定的底座注册表（只读；诊断与桌面隔离测试用）。
	Runtime *runtime.Registry
	// Planes 只读的领域平面集合（doctor / 装配断言用）。
	// **生产行为路径不得从这里取事实**：行为面只认用例接口与端口。
	Planes *registry.Planes
}

// BuildResult 一次装配的产物：**只暴露端口**（阶段 0.5）。
//
// 不再出现 `*registry.Planes` / `*run.Service` / `*runtimeconn.Plane` 等具体类型：
// 应用壳只经 `Handler` 起 HTTP、经 `Probe` 读诊断、经 `Close` 收尾。
type BuildResult struct {
	Handler     Handler
	Maintenance Maintenance
	Probe       Probe
	closeOnce   sync.Once
	stop        func()
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

	// 产品装配：**只注册构建期选中的那一个底座**（见 pkg/runtime/build.go）。
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

	as := &assembly{
		cfg:     cfg,
		sources: sources,
		opts:    o,
		mode:    cfg.ResolvedStoreMode(),
		reg:     reg,
		bus:     eventbus.New(),
	}
	// 执行面 + 领域平面 + 五个来源接线由**同一份**最小装配产出（与 `mate demo` 共用）：
	// 两半各自记账会让"诊断说 stopped、连接却活着"立刻发生；来源漏接是静默的。
	as.svc, as.planes, as.rconn, as.rpool = NewSessionServiceWith(reg, as.bus, o.ConnPool)

	as.creds = o.Credentials
	as.lifecycle = registry.New(as.steps()...)
	if err := as.lifecycle.Start(ctx); err != nil {
		return nil, err
	}

	res := &BuildResult{
		Handler:     Handler{Handler: as.engine, Listener: as.ln},
		Maintenance: Maintenance{Services: as.services},
		Probe: Probe{
			Meta: func() dtosystem.MetaInfo { return as.services.Meta.Meta() },
			AutoProbeMCP: func(ctx context.Context) error {
				return as.planes.Mcp.AutoProbe(ctx)
			},
			Runtime: as.reg,
			Planes:  as.planes,
		},
	}
	res.stop = func() { as.lifecycle.Stop(context.Background()) }
	return res, nil
}
