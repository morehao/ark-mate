// Package app 是控制面应用的装配层（对齐 ark-iam 的 apps/<app>/app.go）。
//
// 职责边界：
//   - cmd/mate：进程级分发（把参数交给 command.Run，把返回码交还 shell）；
//   - internal/app/command：子命令参数解析与命令行输出；
//   - 本包：按解析后的配置装配运行时依赖，并持有启停生命周期；
//   - internal/registry：领域平面的唯一构造点 + 启停声明序列（P5a）；
//   - internal/router + internal/controller：只认依赖事实与用例端口，不感知装配。
//
// 装配顺序是有语义的（持久层先于编排器恢复、工作空间策略与定义先于路由、空闲回收与 MCP
// 自动探测在 HTTP 就绪后才允许后台跑）。P5a 起这条顺序是 `assembly.steps()` 里的**声明表**，
// 不再是 `Init` 的控制流；逐条对照表见 `.dsh/docs/plans/2026-10-17-p5a-registry-assembly-table.md`。
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/registry"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/pkg/config"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ExitError 携带进程退出码的装配错误。
//
// 为什么不是直接 os.Exit：装配层是可被测试与复用的代码，抛错比杀进程可控；
// 退出码策略留在命令层，且与改造前的取值逐一对应（配置类错误 2、运行期错误 1）。
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// assetErr 资产错误（退出码 3，M2-1）：前端资产缺失/不可用。
//
// 为什么单给一个码：它与"配置写错"（2）和"运行期故障"（1）都不同——**部署形态装错了**。
// 桌面壳据此区分"壳的 --web-dir 指错了"与"核心自己崩了"，也给运维一条可判定的信号。
func assetErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 3, Err: fmt.Errorf(format, args...)}
}

// usageErr 用法/配置错误（退出码 2）。
func usageErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, args...)}
}

// fatalErr 运行期错误（退出码 1）。
func fatalErr(format string, args ...any) *ExitError {
	return &ExitError{Code: 1, Err: fmt.Errorf(format, args...)}
}

// App 控制面应用：持有装配好的依赖与 HTTP 生命周期。
type App struct {
	cfg config.Config
	// sources 记录每个配置键的最终来源（用于判断工作区白名单是否被更高优先级来源锁定）。
	sources config.Sources

	bus  *eventbus.Bus
	svc  *sessionruntime.Service
	orch *sessionruntime.Orchestrator
	// planes 领域平面的**唯一实例**（P5a；核心、网关子进程与 doctor 共用同一套构造）。
	planes *registry.Planes
	// lifecycle 本次启停的声明序列（Close 按它的逆序执行）。
	lifecycle *registry.Sequence

	// rconn 本次装配的**执行面**（底座绑定 + 诊断状态 + 连接键派生）。
	// 它由装配层造、同一个实例交给 `svc`（记账）与 API 面 `svcruntime`（读面）——见 WithRuntimePlane。
	rconn *runtimeconn.Plane
	// reg 是本次装配的 runtime 注册表（内置 + runtimes.json 的远程端点）。
	// 留着它是为了在装载定义时能枚举「有哪些 runtime」——skill 投影目标是从**声明**推出来的，
	// 不是从配置里读出来的（配置只是覆盖）。
	reg *runtime.Registry
	// st 仅在 sqlite 模式非空；memory 模式下事实源是 transcript 文件。
	st  *store.Store
	srv *httpServer

	// creds 凭据表（M1-1）：桌面态由命令层经 --auth-stdin 注入；独立态由 MATE_TOKEN 折叠而来。
	// **不进 config.Config**——配置面会被 `mate config --json` 打印出来，令牌不该出现在那里。
	creds *credential.Set

	// readyStdout 是否在监听成功后往 stdout 打一行 `MATE_READY {...}`（M1-2，桌面壳据此拿地址）。
	readyStdout bool

	// payloads 桌面态已解析的随包运行时载荷清单（M3-4）：**唯一真源是壳**（只有它做了
	// 显式配置 → 随包 → PATH 的三级解析，设计 §6.5），壳经 `--payloads` 交给核心，
	// 核心只把它透出到 `/meta` 的诊断面。非桌面态为 nil → `/meta` 照发空数组。
	payloads []ctrsystem.PayloadInfo

	// assets 本次装配解析出的前端资产（M2-1；供 /meta 与诊断使用）。
	assets router.AssetInfo

	// assetWatch 开发期资产重建通知（P6-A）：只有 `MATE_ASSET_WATCH`/`--asset-watch` 给了
	// 正时长时才非 nil。nil 时**不注册** `/dev/assets/events`（交付形态的默认）。
	assetWatch *router.AssetWatcher

	closeOnce sync.Once
}

// Option 装配可选项（M1-1/M1-2）。
type Option func(*App)

// WithCredentials 注入凭据表（桌面态双凭据；独立态通常不传，走 MATE_TOKEN 折叠）。
func WithCredentials(creds *credential.Set) Option {
	return func(a *App) { a.creds = creds }
}

// WithReadyStdout 开启就绪行回写（桌面壳 spawn 核心时传 `--ready-stdout`）。
func WithReadyStdout(on bool) Option {
	return func(a *App) { a.readyStdout = on }
}

// WithPayloads 注入桌面壳**已解析**的随包载荷清单（M3-4；壳经 `--payloads` 交来）。
//
// 只收结构化值：JSON 解析与校验在命令层（照 WithCredentials 的分层），
// 本包因此既不需要认识 wire 形状，也不会因为"某一层少校验一条"而透出假来源。
func WithPayloads(payloads []ctrsystem.PayloadInfo) Option {
	return func(a *App) { a.payloads = payloads }
}

// New 构造 App（只记录配置与可选项，不做任何 IO）。
func New(cfg config.Config, sources config.Sources, opts ...Option) *App {
	a := &App{cfg: cfg, sources: sources}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Engine 返回底层 gin.Engine（供测试挂载/断言路由）。
func (a *App) Engine() *gin.Engine {
	if a.srv == nil {
		return nil
	}
	return a.srv.Engine()
}

// Init 装配全部运行时依赖。任何失败都返回带退出码的 *ExitError，由命令层决定进程结局。
//
// 本函数只做**构造**（注册表 / 事件总线 / 领域平面 / 执行面 / 用例聚合）；所有"按顺序生效"
// 的动作都在 `assembly.steps()` 的声明表里。critical 步骤失败会逆序回滚已启动的步骤。
func (a *App) Init(ctx context.Context) error {
	cfg := a.cfg

	// 产品装配：**只注册构建期选中的那一个底座**（见 pkg/runtime/build.go）。
	// 远程 runtime（A5）：runtimes.json 声明的远程 ACP 端点参与同一次选择——选中的 id 命中其中
	// 一条声明时实现来自远端；未选中的声明不进入本进程的注册表（它们是**候选**，不是装配结果）。
	// 与内置 id 冲突、或选中的 id 谁都不是 → 启动即失败（不静默覆盖内置，也不给出空注册表）。
	remoteSpecs := make([]runtime.Spec, 0, len(cfg.RemoteRuntimes))
	for _, rt := range cfg.RemoteRuntimes {
		remoteSpecs = append(remoteSpecs, runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
	}
	reg, err := runtime.ProductRegistry(remoteSpecs)
	if err != nil {
		return usageErr("serve: %v", err)
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

	a.bus = eventbus.New()
	// P4a：执行面由**装配层**造，同一个实例同时交给 Service（记账）与 API 面（读面）——
	// 两个平面各自记账会让"诊断说 stopped、连接却活着"立刻发生。
	a.rconn = runtimeconn.NewPlaneFor(reg)
	// P5a：领域平面由 registry 造**一次**，核心与网关共用同一套（不再各自 sessionruntime.NewService）。
	a.planes = registry.NewPlanes(reg)
	a.svc = sessionruntime.NewService(reg, a.bus, sessionruntime.WithPlanes(a.planes), sessionruntime.WithRuntimePlane(a.rconn))
	a.reg = reg

	asm := &assembly{a: a, cfg: cfg, mode: cfg.ResolvedStoreMode()}
	a.lifecycle = registry.New(asm.steps()...)
	return a.lifecycle.Start(ctx)
}

// credentials 解析本次进程的凭据表（M1-1）。
//
// 两个来源，优先级明确：
//  1. **命令层注入**（桌面态 `--auth-stdin` 的双凭据）：显式来源，最高优先级；
//  2. **MATE_TOKEN**（独立态历史行为）：折叠成一条同时具备 rest 与 stream 两个面的凭据，
//     因此"同一个令牌既能调 REST 也能订阅事件流"的既有语义逐字保留。
//
// 两者同时存在时以注入为准并告警：桌面态的核心由壳 spawn，用户 shell 里残留的
// MATE_TOKEN 不该改变桌面态的凭据分层（它只该管独立态）。
func (a *App) credentials() *credential.Set {
	if a.creds.Required() {
		if a.cfg.Token != "" {
			glog.Warnw(context.Background(), "serve: --auth-stdin 与 MATE_TOKEN 同时存在，以 --auth-stdin 的双凭据为准（MATE_TOKEN 被忽略）")
		}
		return a.creds
	}
	if a.cfg.Token != "" {
		return credential.MustNew(credential.Pair{
			Token:  a.cfg.Token,
			Scopes: credential.ScopeREST | credential.ScopeStream,
		})
	}
	return nil
}

// metaInfo 组装 `/v1/mate/meta` 的诊断值（M1-7）。
//
// 资产相关字段（webPath / webDigest / webBuildId）由 router 从本次装配解析出的资产补，
// 载荷清单（M3-4）来自壳经 `--payloads` 交来的已解析结果——核心不自己解析载荷，
// 因为在桌面安装里**只有壳**做了显式配置 → 随包 → PATH 的三级解析（设计 §6.5）。
func (a *App) metaInfo() ctrsystem.MetaSource {
	// 数据目录直接用**已解析的键**（M3-3 起 MATE_HOME 是独立键，默认 ~/.ark-mate）。
	// 不再从配置目录用 filepath.Dir 反推：MATE_CONFIG_DIR 可以让配置目录与数据目录分家
	// （测试、容器挂载都用得到），反推会报出一个用户根本没在用的目录。
	payloads := a.payloads
	if payloads == nil {
		// 非桌面态：恒为空数组而不是 nil，让直接读 metaInfo() 的调用方与 HTTP 形状一致。
		payloads = []ctrsystem.PayloadInfo{}
	}
	return ctrsystem.MetaSource{
		CoreVersion: version.Version,
		APIContract: version.APIContract,
		Home:        a.cfg.HomeDir,
		Payloads:    payloads,
	}
}

// loadDefinitions 装载 Agent / 能力定义并重校 skill 物化。
//
// 都放在这里而不是各自的构造里：它们是"配置即事实源"的装载点，
// 失败只告警不阻断（用户目录缺文件是常态），但必须发生在路由起来之前。
func (a *App) loadDefinitions(ctx context.Context, mode string) error {
	cfg := a.cfg
	// provider 档案（P2 / v11）：**按持久层模式分派**——sqlite 模式下档案在 `providers` 表里，
	// memory 模式仍是 `<ProviderDir>/providers.json`。两种模式都要拿到 `cfg.ProviderDir`：
	// providers.json 仍在文件侧（计划 §7 P-9）：memory 模式下它是事实源，sqlite 模式下它是
	// 导入/导出载体（v20 起它**不再是任何密钥的锚点**——密钥在库与 `<MATE_HOME>/credentials.json`）。
	if mode == config.StoreModeSQLite {
		a.planes.Providers.LoadProviderDefinitionsFromStore(ctx, cfg.ProviderDir)
	} else {
		a.planes.Providers.LoadProviderDefinitions(cfg.ProviderDir)
	}
	// 能力数据面（P1 / M6-2）：**按持久层模式分派**——sqlite 模式下 DB 是事实源（v8 的
	// skills/mcp_servers），memory 模式下文件仍是装载源。
	//
	// 为什么 memory 模式不跟着换：memory 模式没有持久层，文件是它唯一的持久形态；不给它造
	// 第二套存储，也不让能力静默变空表（设计文档 §1.4 C-1 的精确措辞：sqlite 模式下"不支持
	// 手工改文件"是结构性事实，memory 模式下那条仍是约定）。默认（make dev/server）是 sqlite。
	//
	// `capabilityDir`（MATE_CAPABILITIES）在 sqlite 模式下**降级为导入/导出目录**：
	// 存量定义还在那里，由 M6-4 的导入器一次性灌进 DB；`mate capability import|export` 也读它。
	if mode == config.StoreModeSQLite {
		a.planes.LoadCapabilityDefinitionsFromStore(ctx, cfg.CapabilityDir)
	} else {
		a.planes.LoadCapabilityDefinitions(cfg.CapabilityDir)
	}
	// automation 定义（M6-7）：同样**按持久层模式分派**——sqlite 模式下定义与运行态都在
	// `automations` 表里（存量文件由 M6-7 的导入器一次性灌进去），memory 模式仍是
	// `MATE_AUTOMATIONS/<id>.json`（定义与运行态同文件）。
	if mode == config.StoreModeSQLite {
		a.planes.Automation.LoadAutomationDefinitionsFromStore(ctx, cfg.AutomationDir)
	} else {
		a.planes.Automation.LoadAutomationDefinitions(cfg.AutomationDir)
	}
	// skill 投影（§16.5 切片 D / design §5.4）：把注册表里的 skill 投影到各 runtime 的原生目录。
	// 目标根与形态来自 **runtime 的投影声明**；MATE_PROJECTION_DIRS 只在需要覆盖根时才出现。
	a.reconcileSkillProjections()
	return nil
}

// reconcileSkillProjections 枚举已注册 runtime，逐个解析 skill 投影并重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先**全解析一遍**（含失败项），无论成败都登记进 manager——登记是审计视图的前提，
//     解析失败的 runtime 必须以 not_configured 的形式可见，而不是从列表里消失；
//  2. 再对解析成功的逐个重校（解析失败的重校直接短路）；
//  3. 最后把**没人认领的覆盖项**告警出来——MATE_PROJECTION_DIRS 里写了个没注册过的 runtime
//     id 时，配置看着生效了、实际一次都没用过；这属于必须说出来的静默失效。
func (a *App) reconcileSkillProjections() {
	specs := a.reg.List()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	projections := make([]skill.Projection, 0, len(specs))
	claimed := make(map[string]bool, len(specs))
	for _, sp := range specs {
		claimed[sp.ID+":"+string(runtime.ProjectionSkill)] = true
		p, ok, err := runtime.Resolve(sp, runtime.ProjectionSkill, a.cfg.ProjectionDirs, nil)
		switch {
		case err != nil:
			glog.Warnw(context.Background(), "serve: skill 投影解析失败（该 runtime 不会收到任何技能）", "runtime", sp.ID, "err", err)
			projections = append(projections, skill.Projection{Runtime: sp.ID, Err: err.Error()})
			continue
		case !ok:
			// 该 runtime 没声明 skill 投影（远程端点即此）：不是错误，什么都不做。
			continue
		}
		proj := skill.Projection{Runtime: sp.ID, Projection: p}
		w, r, mErr := a.planes.Skills.ReconcileSkillProjection(proj)
		if mErr != nil {
			glog.Warnw(context.Background(), "serve: skill 物化重校失败", "runtime", sp.ID, "root", p.Root, "err", mErr)
		} else if w > 0 || r > 0 {
			glog.Infow(context.Background(), "serve: skill 物化重校完成", "runtime", sp.ID, "root", p.Root, "written", w, "removed", r)
		}
		if !p.Tested {
			glog.Infow(context.Background(), "serve: skill 投影形态尚未端到端实测（产物会写，但底座是否读得到未验证）",
				"runtime", sp.ID, "root", p.Root, "note", p.UnverifiedReason())
		}
		projections = append(projections, proj)
	}
	a.planes.Skills.Projections().Set(projections)
	for _, key := range sortedKeys(a.cfg.ProjectionDirs) {
		if !claimed[key] {
			glog.Warnw(context.Background(), "serve: MATE_PROJECTION_DIRS 的这一项没有对应的已注册 runtime:skill，未生效（拼写？该 runtime 未注册？）",
				"key", key, "dir", a.cfg.ProjectionDirs[key])
		}
	}
}

// sortedKeys map 键的稳定顺序（日志与执行顺序可复现）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Run 启动 HTTP 服务并阻塞至 ctx 结束（信号或调用方取消），随后优雅停机。
func (a *App) Run(ctx context.Context) error {
	if a.srv == nil {
		return errors.New("app: Run 前必须先 Init")
	}
	// 优雅退出：ctx 结束 → 停 HTTP；返回后由 Close 关编排器（回收 runtime 子进程）。
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		_ = a.srv.Stop(stopCtx)
	}()

	// MCP 自动探测（B4；MATE_MCP_AUTO_PROBE=startup，默认 off）：HTTP 起来后后台逐个探测，
	// 不阻塞服务启动；结果与显式 POST /mcps/:id/probe 一致（写 Tool 目录 + 探测状态）。
	//
	// P4a 正文第三十八刀：`manager` 那两条转发已删，这里直接经**事实出口** `McpPlane()` 取平面
	// （与 `a.svc.Planes().Picker.NativePickerMode()` 同一形状：装配点交事实，不交转发）。
	if a.cfg.McpAutoProbe == config.McpAutoProbeStartup {
		go func() {
			glog.Infow(ctx, "serve: MCP 自动探测已启用（startup 模式，后台逐个探测已注册 server）")
			_ = a.planes.Mcp.AutoProbe(ctx)
		}()
	}

	// 就绪行（M1-2）：监听器在 Init 期就绑好了，这里打的是**实际**地址（`:0` 时是内核分配的端口）。
	// 只走 stdout 一行、不含任何令牌；壳按行首 `MATE_READY ` 匹配，拿不到就退回 health 轮询。
	if a.readyStdout {
		if line, err := ReadyLine(a.srv.Addr()); err != nil {
			glog.Warnw(ctx, "serve: 组装就绪行失败，壳将退回 health 轮询", "err", err)
		} else {
			fmt.Println(line)
		}
	}

	if err := a.srv.Start(); err != nil {
		return fatalErr("serve: %v", err)
	}
	return nil
}

// Close 释放装配的资源。顺序与改造前 defer 的 LIFO 一致：先关编排器（回收子进程），再关持久层。
func (a *App) Close() {
	a.closeOnce.Do(func() {
		// 轮询循环先停：它只读资产目录，但让它在关停过程中继续推帧/打日志没有意义
		// （Stop 幂等，且 ctx 也会兜住——这里是为了"不需要依赖 ctx 时序"）。
		if a.assetWatch != nil {
			a.assetWatch.Stop()
		}
		if a.svc != nil {
			a.svc.Planes().Automation.StopAutomationScheduler()
		}
		if a.orch != nil {
			a.orch.Close()
		}
		if a.st != nil {
			_ = a.st.Close()
		}
	})
}
