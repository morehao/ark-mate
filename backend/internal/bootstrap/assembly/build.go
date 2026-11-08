package assembly

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/ark-mate/internal/infra/dsh"
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

	// dsh office/依赖内置能力（方案 A）：平台自建持久 profile `mate-acp`（基于 acp 模板 + office 覆写层），
	// 并把随包 runtime 根派生成 DSH_PRIMARY_RUNTIME / DSH_BUNDLED_OFFICE_* 注入该底座启动环境。
	//  - profile 落点由 homeDir 拼（<MATE_HOME>/runtime/dsh/profiles/mate-acp），与 DSH_HOME 隔离声明一致；
	//  - 只对**选中且承载能力面**的底座做（远程端点不拉进程）。
	// 失败只记 WARN、不拒绝启动：缺 office 能力时 agent 退回自带工具链，不该让整个应用起不来。
	if spec, ok := reg.Get(runtime.SelectedRuntime()); ok {
		// profile 落点由 homeDir 拼（<MATE_HOME>/runtime/dsh/profiles/mate-acp），与 DSH_HOME 隔离声明一致。
		// 只在**本机拉子进程**的底座上做：远程端点不读 Launch.Env、也不该被塞 env。
		if runtime.LaunchesLocally(spec) {
			if written, perr := dsh.ProvisionProfile(cfg.HomeDir); perr != nil {
				glog.Warnw(ctx, "serve: 初始化 mate-acp profile 失败（office 能力可能不可用）", "err", perr)
			} else {
				glog.Infow(ctx, "serve: mate-acp profile 就绪", "dir", config.DisplayHome(dsh.ProfileDir(cfg.HomeDir)), "files_written", written)
			}
			if root := dshBundledRuntimeRoot(cfg); root != "" {
				id := runtime.SelectedRuntime()
				for _, kv := range dsh.BundledEnv(root) {
					eq := strings.IndexByte(kv, '=')
					if eq < 0 {
						continue
					}
					if err := reg.SetLaunchEnv(id, kv[:eq], kv[eq+1:]); err != nil {
						glog.Warnw(ctx, "serve: 注入 dsh office 随包 env 失败", "key", kv[:eq], "err", err)
					}
				}
				glog.Infow(ctx, "serve: 已注入 dsh office 随包 env", "runtime", id, "bundled_root", config.DisplayHome(root))
			} else {
				glog.Warnw(ctx, "serve: 未找到 dsh 随包 runtime 根，office 能力将不可用（可用 MATE_DSH_BUNDLED_RUNTIME 指定）")
			}
		}
	}

	as := &assembly{
		cfg:     cfg,
		sources: sources,
		opts:    o,
		mode:    cfg.ResolvedStoreMode(),
		reg:     reg,
		bus:     eventbus.New(),
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

// dshBundledRuntimeRoot 返回 dsh 随包 runtime 根（`Resources/runtime`），供注入 office 能力 env。
//
// 取值优先级（逐级回退，任一命中即返回）：
//  1. `MATE_DSH_BUNDLED_RUNTIME`（显式覆盖，最高优先）；
//  2. 由 PATH 里 dsh 可执行位置反推——**必须 EvalSymlinks**：dev 态 dsh 常是 `.cache/dev-bin/dsh`
//     shim（`exec /usr/local/bin/dsh`）或 `/usr/local/bin/dsh` 软链，`LookPath` 拿到的是 shim/软链
//     本身，不 follow 就会把根算错（实测坑：直接 Dir 三层得到 `…/.cache`，`base!=runtime` → 空）。
//     跟随软链后指向 `<root>/runtime/cli/bin/dsh`，`<root>/runtime` 即答案；
//  3. 已知安装位置 `/Applications/DeepSeek Harness.app/Contents/Resources/runtime`（macOS 打包态兜底）。
//
// 全部落空（如 npx / 裸 npm 装的 dsh、或非 macOS）→ 返回空，调用方跳过注入（保持"无 office 能力"。
// 现状，绝不注入一个错路径让底座 ENOENT）。
func dshBundledRuntimeRoot(cfg config.Config) string {
	if v := strings.TrimSpace(os.Getenv("MATE_DSH_BUNDLED_RUNTIME")); v != "" {
		return v
	}
	// 由 `dsh` 命令解析出的绝对路径（跟随软链）反推 Resources/runtime 根。
	if p, err := exec.LookPath(dsh.Command); err == nil && p != "" {
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil && real != "" {
			p = real
		}
		// 期望布局：<root>/runtime/cli/bin/dsh
		bin := filepath.Dir(p)    // .../cli/bin
		cli := filepath.Dir(bin)  // .../cli
		root := filepath.Dir(cli) // .../runtime
		if filepath.Base(root) == "runtime" {
			return root
		}
	}
	// 打包态兜底：macOS 上随包 runtime 的已知绝对位置。
	const macRuntime = "/Applications/DeepSeek Harness.app/Contents/Resources/runtime"
	if fi, err := os.Stat(filepath.Join(macRuntime, "primary-runtime", "runtime.json")); err == nil && !fi.IsDir() {
		return macRuntime
	}
	return ""
}
