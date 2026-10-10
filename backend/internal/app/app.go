// Package app 是控制面应用的装配层（对齐 ark-iam 的 apps/<app>/app.go）。
//
// 职责边界：
//   - cmd/mate：进程级分发（把参数交给 command.Run，把返回码交还 shell）；
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
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/pkg/config"
	"github.com/morehao/ark-mate/pkg/procreg"
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
	svc  *manager.Service
	orch *manager.Orchestrator
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

// Service 返回领域服务实现（供命令层做迁移等直连操作，如 mate demo）。
func (a *App) Service() *manager.Service { return a.svc }

// Orchestrator 返回编排器（供命令层直接驱动一次性会话，如 mate demo）。
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
		glog.Infow(ctx, "serve: 已注入 runtime 配置隔离环境", "home", cfg.HomeDir, "runtimes", ids)
	}

	a.bus = eventbus.New()
	a.svc = manager.NewService(reg, a.bus)
	a.reg = reg

	// 持久层装配（P1）：MATE_STORE=sqlite → SQLite（DB 为事实源，transcript 停写保留只读）；
	// =memory → 维持 transcript 落盘 + 启动回读；auto（默认）→ DB 文件已存在则 sqlite，否则 memory。
	mode := cfg.ResolvedStoreMode()
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
		// 能力定义一次性导入（M6-4）：v8 之后 DB 才是能力面的事实源，而存量定义还在
		// `MATE_CAPABILITIES` 目录里。**必须发生在 loadDefinitions 之前**——装载只从 DB 读，
		// 导入晚了这一轮启动的能力面就是空的（用户看到"我的技能全没了"，哪怕下一轮就回来）。
		//
		// 幂等靠目录指纹（`meta.imported_capabilities`），且**按行判重**（导入绝不覆盖）：
		// 目录没变 → 一个字节都不写；目录变了 → 只补缺失的行。失败只 WARN 不阻断启动
		// （能力面为空是可见的，而"起不来"是更坏的一种失败），出路是显式 `mate capability import`。
		if imp, iErr := manager.ImportCapabilityDir(ctx, st, cfg.CapabilityDir,
			manager.DefinitionImportOptions{Auto: true}, nil); iErr != nil {
			glog.Warnw(ctx, "serve: 导入存量能力定义失败（能力面可能为空；可跑 `mate capability import` 重试）",
				"err", iErr, "dir", cfg.CapabilityDir)
		} else if !imp.Already && imp.Inserted+imp.Existing+imp.Invalid+imp.Tools > 0 {
			glog.Infow(ctx, "serve: 存量能力定义导入完成",
				"dir", imp.Dir, "skills", imp.Skills, "mcps", imp.Mcps,
				"inserted", imp.Inserted, "existing", imp.Existing,
				"invalid", imp.Invalid, "tools_files_skipped", imp.Tools)
		}
		// agent 定义一次性导入（M6-6）：与上面能力面**同一次触发、同一个理由**（v9 之后 DB 才是
		// agent 定义的事实源，而存量定义还在 `MATE_AGENTS` 里），也必须发生在 loadDefinitions 之前。
		// 目录里的 `providers.json`（档案留在文件侧）与 `default.json`（显式选择进 meta）不参与导入。
		//
		// 两个域各自记指纹（`meta.imported_agents`）：只改 agent 目录不该让能力面的导入重跑一遍。
		if imp, iErr := manager.ImportAgentDir(ctx, st, cfg.AgentDir,
			manager.DefinitionImportOptions{Auto: true}, nil); iErr != nil {
			glog.Warnw(ctx, "serve: 导入存量 agent 定义失败（agent 面可能只剩内置种子；可跑 `mate agent import` 重试）",
				"err", iErr, "dir", cfg.AgentDir)
		} else if !imp.Already && imp.Agents+imp.Inserted+imp.Existing+imp.Invalid > 0 {
			glog.Infow(ctx, "serve: 存量 agent 定义导入完成",
				"dir", imp.Dir, "agents", imp.Agents, "inserted", imp.Inserted,
				"existing", imp.Existing, "invalid", imp.Invalid, "default_agent", imp.DefaultAgent)
		}
		// automation 定义一次性导入（M6-7）：与上面两个域**同一次触发、同一个理由**（v10 之后
		// DB 才是定时任务的事实源，而存量定义还在 `MATE_AUTOMATIONS` 里）。automation 的定义
		// 文件里还住着运行态（`last_fired_at`/`recent_runs`），导入会按保留键把它们切进**运行态列**
		// ——所以搬运是"一个文件 → 一行里的两类列"，同一事务，不出现半截状态。
		//
		// 三个域各自记指纹（`meta.imported_automations`）：只改 automation 目录不该让另外两个域重跑。
		if imp, iErr := manager.ImportAutomationDir(ctx, st, cfg.AutomationDir,
			manager.DefinitionImportOptions{Auto: true}, nil); iErr != nil {
			glog.Warnw(ctx, "serve: 导入存量 automation 定义失败（定时任务面可能为空；可跑 `mate automation import` 重试）",
				"err", iErr, "dir", cfg.AutomationDir)
		} else if !imp.Already && imp.Automations+imp.Inserted+imp.Existing+imp.Invalid > 0 {
			glog.Infow(ctx, "serve: 存量 automation 定义导入完成",
				"dir", imp.Dir, "automations", imp.Automations, "inserted", imp.Inserted,
				"existing", imp.Existing, "invalid", imp.Invalid)
		}
		// provider 档案一次性导入（v11，设置 · 模型）：与上面三个域**同一次触发**（v11 之后 DB 才是
		// 档案的事实源，而存量档案还在 `<AgentDir>/providers.json` 里），也要发生在 loadDefinitions
		// 之前——**agent 装载要能引用到它**（`CredentialProblem` 的悬空引用判据在装载/拨号两条路上）。
		//
		// 与其余三个域的两处形态差异：① 幂等依据是**文件内容指纹**（整个域只有一个文件，而它的内容
		// 就是全部事实）；② 迁移前**备份** `providers.json.bak-v11`（这是全篇唯一一个不可逆动作——
		// 回退窗口在导入完成后关闭），备份失败即拒绝导入（无备份就没有回退路径）。
		if imp, iErr := manager.ImportProviderFile(ctx, st, cfg.AgentDir,
			manager.DefinitionImportOptions{Auto: true}, nil); iErr != nil {
			glog.Warnw(ctx, "serve: 导入存量 provider 档案失败（供应商面可能为空；可跑 `mate provider import` 重试）",
				"err", iErr, "dir", cfg.AgentDir)
		} else if !imp.Already && imp.Providers+imp.Inserted+imp.Existing > 0 {
			glog.Infow(ctx, "serve: 存量 provider 档案导入完成",
				"dir", imp.Dir, "providers", imp.Providers, "inserted", imp.Inserted,
				"existing", imp.Existing, "backup", imp.Backup, "plaintext_left", imp.Plaintext)
		}
		conversations, events, rErr := a.svc.RestoreFromStore(ctx)
		if rErr != nil {
			glog.Warnw(ctx, "serve: 从持久层恢复失败", "err", rErr)
		}
		// transcript 未迁移提示：DB 为空但 transcript 目录有文件 → 提示先 `mate migrate`。
		if conversations == 0 && events == 0 {
			if files, gErr := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl")); gErr == nil && len(files) > 0 {
				fmt.Fprintf(os.Stderr, "serve: %s 下仍有 %d 个 transcript 未导入；运行 `mate migrate` 后重启即可在 sqlite 模式下看到旧会话\n", cfg.TranscriptDir, len(files))
			}
		}
	case config.StoreModeMemory, "":
		// 会话 wire transcript（M1 #5，~/.ark-mate/transcripts 或 MATE_TRANSCRIPTS）。
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
			glog.Warnw(ctx, "serve: 打开进程注册表失败（孤儿回收已禁用）", "path", p, "err", prErr)
		} else {
			a.orch.SetProcRegistry(pr)
			if rep, rErr := a.orch.ReapOrphans(cfg.ReapGrace); rErr != nil {
				glog.Warnw(ctx, "serve: 孤儿进程回收失败", "err", rErr)
			} else if rep.Total() > 0 {
				glog.Infow(ctx, "serve: 孤儿进程回收", "reaped", len(rep.Reaped),
					"cleaned", len(rep.Cleaned), "skipped", len(rep.Skipped), "registry", p)
			}
		}
	}

	if err := a.loadDefinitions(ctx, mode); err != nil {
		return err
	}

	if mode == config.StoreModeSQLite {
		// 启动恢复会话级权限记忆（permission_memories 表 → perm.mem，allow/reject 双侧）。
		if err := a.orch.RestoreMemoriesFromStore(ctx); err != nil {
			glog.Warnw(ctx, "serve: 恢复权限记忆失败", "err", err)
		}
	}
	if cfg.TurnTimeout > 0 {
		a.orch.SetTurnTimeout(cfg.TurnTimeout)
	}
	// handoff memo 字符预算（P3 F3；MATE_HANDOFF_MAX_CHARS 覆盖，默认 6000）。
	a.orch.SetHandoffMaxChars(cfg.HandoffMaxChars)
	// 能力代际上限（M3-7；MATE_GENERATION_LIMIT，0 = 不限制）。
	a.orch.SetGenerationLimit(cfg.GenerationLimit)
	// 权限/elicitation 挂起超时（默认 30min；MATE_PERMISSION_TIMEOUT / MATE_ELICITATION_TIMEOUT 覆盖）。
	a.orch.SetPermissionTimeout(cfg.PermissionTimeout)
	a.orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	// 原生权限映射（B5）：MATE_NATIVE_PERMISSION=off 时关闭（默认 auto）。
	// 平台侧 MCP 网关开关（M7-3）：默认 auto（开），只有显式 off 才回退到"远程直连注入"。
	a.svc.SetMcpGatewayEnabled(cfg.McpGatewayEnabled())
	if !cfg.McpGatewayEnabled() {
		glog.Warnw(ctx, "serve: MATE_MCP_GATEWAY=off —— 远程 MCP 回到直连注入（按底座承载能力过滤）")
	}
	if cfg.NativePermission == config.NativePermissionOff {
		a.svc.SetNativePermission(false)
		glog.Infow(ctx, "serve: 原生权限映射已关闭（MATE_NATIVE_PERMISSION=off）")
	}
	// 空闲回收（P2）：StartAgent 暖进程闲置超阈值自动停（MATE_IDLE_TIMEOUT，默认 5m；0 = 关闭）。
	a.orch.StartIdleReaper(cfg.ReapInterval, cfg.IdleTimeout)
	// M4-4 调度循环：`MATE_AUTOMATION_TICK=0` 关闭（装配期同步跑一次启动恢复 pass）。
	a.svc.StartAutomationScheduler(cfg.AutomationTick)

	// 装配各领域服务端口。当前 manager.Service 是单一聚合，同时满足全部端口；
	// 端口声明与实现分离后，装配点成为"谁提供什么能力"的唯一可见处。
	ports := router.Ports{
		Agent: a.svc, Conversation: a.svc, Workspace: a.svc, Runtime: a.svc,
		Capability: a.svc, Automation: a.svc, Library: a.svc, Usage: a.svc, Search: a.svc, Export: a.svc,
	}
	// 资产层（M2-1）：资产来自调用方给的目录（`--web-dir` / `MATE_WEB_DIR`），
	// 没给即**拒绝启动**（退出码 3）。放在监听之前：装错资产时不该先占住端口再报错。
	assets, err := router.ResolveAssets(cfg.WebDir)
	if err != nil {
		return assetErr("serve: %v", err)
	}
	a.assets = assets
	if assets.Has() {
		glog.Infow(ctx, "serve: 前端资产已就绪", "dir", assets.Path)
	}

	// M3-5：先绑定监听器，再用**实际监听地址**构造 Host 白名单 + Origin 矩阵，最后装配路由。
	// 顺序是被判据逼出来的：策略里"同源"的定义就是本进程的监听端口，而桌面态（`127.0.0.1:0`）
	// 的端口由内核分配，只有 net.Listen 之后才存在——配置里的 `:0` 不是判据，
	// 拿它或默认 10001 去比对都是在猜端口。资产解析仍在这之前（装错资产不该先占住端口）。
	ln, err := listen(cfg.Addr)
	if err != nil {
		return usageErr("serve: %v", err)
	}
	originPolicy := middleware.NewOriginPolicy(ln.Addr().String(), cfg.AllowedOrigins)
	if originPolicy.Enabled() {
		glog.Infow(ctx, "serve: 已启用回环 Host 白名单 + Origin 矩阵（M3-5）",
			"listen", ln.Addr().String(), "port", originPolicy.Port(), "allowed_origins", len(cfg.AllowedOrigins))
	} else {
		// 非回环监听的**明确放弃**，不是漏了：设计 §9.4 把 Host/Origin 白名单与
		// "令牌强制 / TLS 终止 / 多操作员审计"并列为对外产品化缺的四件事之一，本期范围外；
		// `--addr` 的 flag 说明也写明对外暴露自担风险。这里必须打 WARN —— 用户以为自己在
		// 对外服务却没有任何 Host/Origin 防护，是那种"不说不出来就永远不知道"的形态。
		glog.Warnw(ctx, "serve: 监听地址不是回环白名单主机（127.0.0.1/::1/localhost），Host 白名单 + Origin 矩阵**未启用**；"+
			"设计 §9.4 明确对外产品化所需的 Host/Origin 白名单不在本期范围，请自行用反向代理/防火墙收口",
			"listen", ln.Addr().String(), "allowed_origins", len(cfg.AllowedOrigins))
	}

	a.srv = newHTTPServerOn(ln, router.New(ports, a.bus, router.Options{
		Credentials: a.credentials(),
		Meta:        a.metaInfo,
		Web:         assets,
		Origin:      originPolicy,
	}))
	if creds := a.credentials(); creds.Required() {
		glog.Infow(ctx, "serve: 已启用凭据鉴权（除 /health 外的 REST 要求 rest 面 Bearer；WS 首帧 attach 要求 stream 面）",
			"entries", creds.Count(), "ws_attach_deadline", credential.StreamAttachDeadline)
	}
	return nil
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
	// Agent definitions（P2 / M6-6）：**按持久层模式分派**——sqlite 模式下定义在 `agents` 表里
	// （种子仍在代码中，行只存"对种子的覆盖"），memory 模式仍是 MATE_AGENTS 目录下的 <id>.json。
	// 两种模式都要拿到 `cfg.AgentDir`：providers.json 与 vault 锚点仍在文件侧（计划 §7 P-9）。
	if mode == config.StoreModeSQLite {
		a.svc.LoadAgentDefinitionsFromStore(ctx, cfg.AgentDir)
	} else {
		a.svc.LoadAgentDefinitions(cfg.AgentDir)
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
		a.svc.LoadCapabilityDefinitionsFromStore(ctx, cfg.CapabilityDir)
	} else {
		a.svc.LoadCapabilityDefinitions(cfg.CapabilityDir)
	}
	// automation 定义（M6-7）：同样**按持久层模式分派**——sqlite 模式下定义与运行态都在
	// `automations` 表里（存量文件由 M6-7 的导入器一次性灌进去），memory 模式仍是
	// `MATE_AUTOMATIONS/<id>.json`（定义与运行态同文件）。
	if mode == config.StoreModeSQLite {
		a.svc.LoadAutomationDefinitionsFromStore(ctx, cfg.AutomationDir)
	} else {
		a.svc.LoadAutomationDefinitions(cfg.AutomationDir)
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
	projections := make([]manager.SkillProjection, 0, len(specs))
	claimed := make(map[string]bool, len(specs))
	for _, sp := range specs {
		claimed[sp.ID+":"+string(runtime.ProjectionSkill)] = true
		p, ok, err := runtime.Resolve(sp, runtime.ProjectionSkill, a.cfg.ProjectionDirs, nil)
		switch {
		case err != nil:
			glog.Warnw(context.Background(), "serve: skill 投影解析失败（该 runtime 不会收到任何技能）", "runtime", sp.ID, "err", err)
			projections = append(projections, manager.SkillProjection{Runtime: sp.ID, Err: err.Error()})
			continue
		case !ok:
			// 该 runtime 没声明 skill 投影（远程端点即此）：不是错误，什么都不做。
			continue
		}
		proj := manager.SkillProjection{Runtime: sp.ID, Projection: p}
		w, r, mErr := a.svc.ReconcileSkillProjection(proj)
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
	a.svc.SetSkillProjections(projections)
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
	if a.cfg.McpAutoProbe == config.McpAutoProbeStartup {
		go func() {
			glog.Infow(ctx, "serve: MCP 自动探测已启用（startup 模式，后台逐个探测已注册 server）")
			_ = a.svc.AutoProbeMcpServers(ctx)
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
