// Package app 是控制面应用的装配层（对齐 ark-iam 的 apps/<app>/app.go）。
//
// 职责边界：
//   - cmd/forge：进程级分发（把参数交给 command.Run，把返回码交还 shell）；
//   - internal/app/command：子命令参数解析与命令行输出；
//   - 本包：按解析后的配置装配运行时依赖（事件总线 / 领域服务 / 编排器 / 持久层 /
//     进程注册表 / 路由引擎），并持有启停生命周期；
//   - internal/router + internal/controller：只认 internal/service 的端口，不感知装配。
//
// 装配顺序是有语义的，不要随意调换：持久层要先于编排器恢复（会话与权限记忆都从库里回读），
// 工作空间策略与能力定义要先于路由起来（首个请求就能看到正确视图），
// 空闲回收与 MCP 自动探测在 HTTP 就绪后才允许后台跑。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrsystem"
	"github.com/morehao/ark-forge/internal/credential"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/router"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/version"
	"github.com/morehao/ark-forge/pkg/config"
	"github.com/morehao/ark-forge/pkg/procreg"
	"github.com/morehao/ark-forge/pkg/runtime"
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
	svc  *manager.Service
	orch *manager.Orchestrator
	// reg 是本次装配的 runtime 注册表（内置 + runtimes.json 的远程端点）。
	// 留着它是为了在装载定义时能枚举「有哪些 runtime」——skill 投影目标是从**声明**推出来的，
	// 不是从配置里读出来的（配置只是覆盖）。
	reg *runtime.Registry
	// st 仅在 sqlite 模式非空；memory 模式下事实源是 transcript 文件。
	st  *store.Store
	srv *httpServer

	// creds 凭据表（M1-1）：桌面态由命令层经 --auth-stdin 注入；独立态由 FORGE_TOKEN 折叠而来。
	// **不进 config.Config**——配置面会被 `forge config --json` 打印出来，令牌不该出现在那里。
	creds *credential.Set

	// readyStdout 是否在监听成功后往 stdout 打一行 `FORGE_READY {...}`（M1-2，桌面壳据此拿地址）。
	readyStdout bool

	// payloads 桌面态已解析的随包运行时载荷清单（M3-4）：**唯一真源是壳**（只有它做了
	// 显式配置 → 随包 → PATH 的三级解析，设计 §6.5），壳经 `--payloads` 交给核心，
	// 核心只把它透出到 `/meta` 的诊断面。非桌面态为 nil → `/meta` 照发空数组。
	payloads []ctrsystem.PayloadInfo

	// assets 本次装配解析出的前端资产（M2-1；供 /meta 与诊断使用）。
	assets router.AssetInfo

	closeOnce sync.Once
}

// Option 装配可选项（M1-1/M1-2）。
type Option func(*App)

// WithCredentials 注入凭据表（桌面态双凭据；独立态通常不传，走 FORGE_TOKEN 折叠）。
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

// Service 返回领域服务实现（供命令层做迁移等直连操作，如 forge demo）。
func (a *App) Service() *manager.Service { return a.svc }

// Orchestrator 返回编排器（供命令层直接驱动一次性会话，如 forge demo）。
func (a *App) Orchestrator() *manager.Orchestrator { return a.orch }

// Engine 返回底层 gin.Engine（供测试挂载/断言路由）。
func (a *App) Engine() *gin.Engine {
	if a.srv == nil {
		return nil
	}
	return a.srv.Engine()
}

// Init 装配全部运行时依赖。任何失败都返回带退出码的 *ExitError，由命令层决定进程结局。
func (a *App) Init(ctx context.Context) error {
	cfg := a.cfg

	reg := runtime.DefaultRegistry()
	// 远程 runtime（A5）：配置目录 runtimes.json 声明的远程 ACP 端点注册进同一注册表，
	// agent 的 runtimes 字段即可引用；与内置 id 冲突时启动即失败（不静默覆盖内置底座）。
	for _, rt := range cfg.RemoteRuntimes {
		if _, exists := reg.Get(rt.ID); exists {
			return usageErr("serve: runtimes.json 的 id %q 与内置 runtime 冲突", rt.ID)
		}
		reg.Register(runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
		slog.Info("serve: 注册远程 runtime", "id", rt.ID, "transport", rt.Transport, "endpoint", rt.Endpoint)
	}

	// runtime 子进程的配置隔离（M3-3；设计 §6.6）：把各底座的 home / cache / npm prefix 重定向到
	// 数据目录下的专属子目录，避免平台拉起的底座去读写用户自己装的那份配置（也避免被它改写）。
	// 只作用于**本地子进程** runtime（远程端点不拉进程，Launch.Env 不会被读），已有键不覆盖——
	// 适配器或 provider 档案显式给的取值优先（见 runtime.Registry.ApplyIsolationEnv）。
	if ids := reg.ApplyIsolationEnv(cfg.HomeDir); len(ids) > 0 {
		slog.Info("serve: 已注入 runtime 配置隔离环境", "home", cfg.HomeDir, "runtimes", ids)
	}

	a.bus = eventbus.New()
	a.svc = manager.NewService(reg, a.bus)
	a.reg = reg

	// 持久层装配（P1）：FORGE_STORE=sqlite → SQLite（DB 为事实源，transcript 停写保留只读）；
	// =memory → 维持 transcript 落盘 + 启动回读；auto（默认）→ DB 文件已存在则 sqlite，否则 memory。
	mode := cfg.StoreMode
	if mode == "" {
		mode = config.StoreModeMemory
		if fi, err := os.Stat(cfg.DBPath); err == nil && fi.Size() > 0 {
			mode = config.StoreModeSQLite
		}
	}
	switch mode {
	case config.StoreModeSQLite:
		// 配置目录化后 DB 路径可由文件指定，父目录未必存在：先补齐（此前只依赖用户手建目录）。
		if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fatalErr("serve: 创建 DB 目录 %s: %v", dir, err)
			}
		}
		st, err := store.Open(cfg.DBPath)
		if err != nil {
			return fatalErr("serve: 打开持久层 %s: %v", cfg.DBPath, err)
		}
		a.st = st
		a.svc.EnableStore(st)
		conversations, events, rErr := a.svc.RestoreFromStore(ctx)
		if rErr != nil {
			slog.Warn("serve: 从持久层恢复失败", "err", rErr)
		}
		// transcript 未迁移提示：DB 为空但 transcript 目录有文件 → 提示先 `forge migrate`。
		if conversations == 0 && events == 0 {
			if files, gErr := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl")); gErr == nil && len(files) > 0 {
				fmt.Fprintf(os.Stderr, "serve: %s 下仍有 %d 个 transcript 未导入；运行 `forge migrate` 后重启即可在 sqlite 模式下看到旧会话\n", cfg.TranscriptDir, len(files))
			}
		}
	case config.StoreModeMemory, "":
		// 会话 wire transcript（M1 #5，~/.forge/transcripts 或 FORGE_TRANSCRIPTS）。
		a.svc.SetTranscriptDir(cfg.TranscriptDir)
	default:
		// 不可达：pkg/config 的 materialize 已按 ValidStoreModes 校验并把 auto 归一成空串，
		// 到这里只可能是配置层与装机层对取值集合的理解出现了 drift。保留为内部错误
		// （不是 usageErr —— 这不是用户输入错，是代码不一致，别让用户去查自己的环境变量），
		// 免得将来 drift 时静默走进某个默认分支。
		return fatalErr("serve: 内部错误：持久层模式 %q 未被处理（配置层校验与装机层取值集合不一致）", mode)
	}

	// 工作空间策略：未指定 cwd 的建会话请求在 <workspace_dir>/<会话id>/ 下代建临时空间
	// （空 = 关闭代建，此时未指定即报 workspace_required，不回落 serve 进程目录）；
	// workspace_roots 非空时把显式 cwd 与目录浏览收在白名单内，代建根**自动并入**生效名单
	// （平台自己的目录本就该天然合法，不再要求运维成对配置）。
	if err := a.svc.SetWorkspacePolicy(cfg.WorkspaceDir, cfg.WorkspaceRoots); err != nil {
		return usageErr("serve: 工作空间策略非法: %v", err)
	}
	// 白名单的持久化端口：面板 PUT /settings/workspace 经它写回配置目录（config.json）。
	// 传入该键的**最终来源**：来源高于配置文件（进程 env / env 文件 / flag）时端口会报"锁定"，
	// 面板随之转只读——这比"面板显示保存成功、重启后被 env 覆盖"诚实。
	a.svc.SetWorkspaceSettingsStore(config.NewSettingsStore(cfg.ConfigDir, a.sources["workspace_roots"]))

	a.orch = manager.NewOrchestrator(a.svc, reg)
	// 进程注册表（M3 v4③）：登记本 daemon 拉起的 runtime 子进程；启动时回收上一实例遗留的
	// 孤儿进程（daemon 被 SIGKILL/崩溃时子进程因独立进程组而存活）。
	if p := cfg.ProcRegistryPath; p != "" {
		pr, prErr := procreg.Open(p)
		if prErr != nil {
			slog.Warn("serve: 打开进程注册表失败（孤儿回收已禁用）", "path", p, "err", prErr)
		} else {
			a.orch.SetProcRegistry(pr)
			if rep, rErr := a.orch.ReapOrphans(cfg.ReapGrace); rErr != nil {
				slog.Warn("serve: 孤儿进程回收失败", "err", rErr)
			} else if rep.Total() > 0 {
				slog.Info("serve: 孤儿进程回收", "reaped", len(rep.Reaped),
					"cleaned", len(rep.Cleaned), "skipped", len(rep.Skipped), "registry", p)
			}
		}
	}

	if err := a.loadDefinitions(); err != nil {
		return err
	}

	if mode == config.StoreModeSQLite {
		// 启动恢复会话级权限记忆（permission_memories 表 → perm.mem，allow/reject 双侧）。
		if err := a.orch.RestoreMemoriesFromStore(ctx); err != nil {
			slog.Warn("serve: 恢复权限记忆失败", "err", err)
		}
	}
	if cfg.TurnTimeout > 0 {
		a.orch.SetTurnTimeout(cfg.TurnTimeout)
	}
	// handoff memo 字符预算（P3 F3；FORGE_HANDOFF_MAX_CHARS 覆盖，默认 6000）。
	a.orch.SetHandoffMaxChars(cfg.HandoffMaxChars)
	// 能力代际上限（M3-7；FORGE_GENERATION_LIMIT，0 = 不限制）。
	a.orch.SetGenerationLimit(cfg.GenerationLimit)
	// 权限/elicitation 挂起超时（默认 30min；FORGE_PERMISSION_TIMEOUT / FORGE_ELICITATION_TIMEOUT 覆盖）。
	a.orch.SetPermissionTimeout(cfg.PermissionTimeout)
	a.orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	// 原生权限映射（B5）：FORGE_NATIVE_PERMISSION=off 时关闭（默认 auto）。
	if cfg.NativePermission == config.NativePermissionOff {
		a.svc.SetNativePermission(false)
		slog.Info("serve: 原生权限映射已关闭（FORGE_NATIVE_PERMISSION=off）")
	}
	// 空闲回收（P2）：StartAgent 暖进程闲置超阈值自动停（FORGE_IDLE_TIMEOUT，默认 5m；0 = 关闭）。
	a.orch.StartIdleReaper(cfg.ReapInterval, cfg.IdleTimeout)
	// M4-4 调度循环：`FORGE_AUTOMATION_TICK=0` 关闭（装配期同步跑一次启动恢复 pass）。
	a.svc.StartAutomationScheduler(cfg.AutomationTick)

	// 装配各领域服务端口。当前 manager.Service 是单一聚合，同时满足全部端口；
	// 端口声明与实现分离后，装配点成为"谁提供什么能力"的唯一可见处。
	ports := router.Ports{
		Agent: a.svc, Conversation: a.svc, Workspace: a.svc, Runtime: a.svc,
		Capability: a.svc, Automation: a.svc, Library: a.svc, Usage: a.svc, Search: a.svc, Export: a.svc,
	}
	// 资产层（M2-1）：显式目录优先、内嵌兜底、两者都没有即**拒绝启动**（退出码 3）。
	// 放在监听之前：装错资产时不该先占住端口再报错。
	assets, err := router.ResolveAssets(cfg.WebDir)
	if err != nil {
		return assetErr("serve: %v", err)
	}
	a.assets = assets
	if assets.Has() {
		where := string(assets.Source)
		if assets.Source == router.AssetSourceDir {
			where += " " + assets.Path
		}
		slog.Info("serve: 前端资产已就绪", "source", where)
	}

	srv, err := newHTTPServer(cfg.Addr, router.New(ports, a.bus, router.Options{
		Credentials: a.credentials(),
		Meta:        a.metaInfo,
		Web:         assets,
	}))
	if err != nil {
		// 端口被占 / 无权限在装配期就报出来（此前是 ListenAndServe 里的运行期错误）。
		return usageErr("serve: %v", err)
	}
	a.srv = srv
	if creds := a.credentials(); creds.Required() {
		slog.Info("serve: 已启用凭据鉴权（除 /health 外的 REST 要求 rest 面 Bearer；WS 首帧 attach 要求 stream 面）",
			"entries", creds.Count(), "ws_attach_deadline", credential.StreamAttachDeadline)
	}
	return nil
}

// credentials 解析本次进程的凭据表（M1-1）。
//
// 两个来源，优先级明确：
//  1. **命令层注入**（桌面态 `--auth-stdin` 的双凭据）：显式来源，最高优先级；
//  2. **FORGE_TOKEN**（独立态历史行为）：折叠成一条同时具备 rest 与 stream 两个面的凭据，
//     因此"同一个令牌既能调 REST 也能订阅事件流"的既有语义逐字保留。
//
// 两者同时存在时以注入为准并告警：桌面态的核心由壳 spawn，用户 shell 里残留的
// FORGE_TOKEN 不该改变桌面态的凭据分层（它只该管独立态）。
func (a *App) credentials() *credential.Set {
	if a.creds.Required() {
		if a.cfg.Token != "" {
			slog.Warn("serve: --auth-stdin 与 FORGE_TOKEN 同时存在，以 --auth-stdin 的双凭据为准（FORGE_TOKEN 被忽略）")
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

// metaInfo 组装 `/v1/forge/meta` 的诊断值（M1-7）。
//
// 资产相关字段（webPath / webDigest / webBuildId）由 router 从本次装配解析出的资产补，
// 载荷清单（M3-4）来自壳经 `--payloads` 交来的已解析结果——核心不自己解析载荷，
// 因为在桌面安装里**只有壳**做了显式配置 → 随包 → PATH 的三级解析（设计 §6.5）。
func (a *App) metaInfo() ctrsystem.MetaSource {
	// 数据目录直接用**已解析的键**（M3-3 起 FORGE_HOME 是独立键，默认 ~/.forge）。
	// 不再从配置目录用 filepath.Dir 反推：FORGE_CONFIG_DIR 可以让配置目录与数据目录分家
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
// 都放在这里而不是各自的构造里：它们是"文件系统即配置"的装载点，
// 失败只告警不阻断（用户目录缺文件是常态），但必须发生在路由起来之前。
func (a *App) loadDefinitions() error {
	cfg := a.cfg
	// Agent definitions（P2）：FORGE_AGENTS 目录下 <id>.json → 注册到 agents 表（服务端无页面时也热加载）。
	a.svc.LoadAgentDefinitions(cfg.AgentDir)
	// 能力数据面（P1）：FORGE_CAPABILITIES 目录下 skills|mcp|tools/*.json → Skill/MCP/Tool 目录。
	a.svc.LoadCapabilityDefinitions(cfg.CapabilityDir)
	a.svc.LoadAutomationDefinitions(cfg.AutomationDir)
	// skill 投影（§16.5 切片 D / design §5.4）：把注册表里的 skill 投影到各 runtime 的原生目录。
	// 目标根与形态来自 **runtime 的投影声明**；FORGE_PROJECTION_DIRS 只在需要覆盖根时才出现。
	a.reconcileSkillProjections()
	return nil
}

// reconcileSkillProjections 枚举已注册 runtime，逐个解析 skill 投影并重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先**全解析一遍**（含失败项），无论成败都登记进 manager——登记是审计视图的前提，
//     解析失败的 runtime 必须以 not_configured 的形式可见，而不是从列表里消失；
//  2. 再对解析成功的逐个重校（解析失败的重校直接短路）；
//  3. 最后把**没人认领的覆盖项**告警出来——FORGE_PROJECTION_DIRS 里写了个没注册过的 runtime
//     id 时，配置看着生效了、实际一次都没用过；这属于必须说出来的静默失效。
func (a *App) reconcileSkillProjections() {
	specs := a.reg.List()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	projections := make([]manager.SkillProjection, 0, len(specs))
	claimed := make(map[string]bool, len(specs))
	for _, sp := range specs {
		claimed[sp.ID+":"+string(runtime.ProjectionSkill)] = true
		p, ok, err := runtime.Resolve(sp, runtime.ProjectionSkill, a.cfg.ProjectionDirs, nil)
		switch {
		case err != nil:
			slog.Warn("serve: skill 投影解析失败（该 runtime 不会收到任何技能）", "runtime", sp.ID, "err", err)
			projections = append(projections, manager.SkillProjection{Runtime: sp.ID, Err: err.Error()})
			continue
		case !ok:
			// 该 runtime 没声明 skill 投影（远程端点即此）：不是错误，什么都不做。
			continue
		}
		proj := manager.SkillProjection{Runtime: sp.ID, Projection: p}
		w, r, mErr := a.svc.ReconcileSkillProjection(proj)
		if mErr != nil {
			slog.Warn("serve: skill 物化重校失败", "runtime", sp.ID, "root", p.Root, "err", mErr)
		} else if w > 0 || r > 0 {
			slog.Info("serve: skill 物化重校完成", "runtime", sp.ID, "root", p.Root, "written", w, "removed", r)
		}
		if !p.Tested {
			slog.Info("serve: skill 投影形态尚未端到端实测（产物会写，但底座是否读得到未验证）",
				"runtime", sp.ID, "root", p.Root, "note", p.UnverifiedReason())
		}
		projections = append(projections, proj)
	}
	a.svc.SetSkillProjections(projections)
	for _, key := range sortedKeys(a.cfg.ProjectionDirs) {
		if !claimed[key] {
			slog.Warn("serve: FORGE_PROJECTION_DIRS 的这一项没有对应的已注册 runtime:skill，未生效（拼写？该 runtime 未注册？）",
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

	// MCP 自动探测（B4；FORGE_MCP_AUTO_PROBE=startup，默认 off）：HTTP 起来后后台逐个探测，
	// 不阻塞服务启动；结果与显式 POST /mcps/:id/probe 一致（写 Tool 目录 + 探测状态）。
	if a.cfg.McpAutoProbe == config.McpAutoProbeStartup {
		go func() {
			slog.Info("serve: MCP 自动探测已启用（startup 模式，后台逐个探测已注册 server）")
			_ = a.svc.AutoProbeMcpServers(ctx)
		}()
	}

	// 就绪行（M1-2）：监听器在 Init 期就绑好了，这里打的是**实际**地址（`:0` 时是内核分配的端口）。
	// 只走 stdout 一行、不含任何令牌；壳按行首 `FORGE_READY ` 匹配，拿不到就退回 health 轮询。
	if a.readyStdout {
		if line, err := ReadyLine(a.srv.Addr()); err != nil {
			slog.Warn("serve: 组装就绪行失败，壳将退回 health 轮询", "err", err)
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
		if a.svc != nil {
			a.svc.StopAutomationScheduler()
		}
		if a.orch != nil {
			a.orch.Close()
		}
		if a.st != nil {
			_ = a.st.Close()
		}
	})
}
