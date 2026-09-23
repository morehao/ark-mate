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
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/router"
	"github.com/morehao/ark-forge/internal/store"
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

	closeOnce sync.Once
}

// New 构造 App（只记录配置，不做任何 IO）。
func New(cfg config.Config, sources config.Sources) *App {
	return &App{cfg: cfg, sources: sources}
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

	// 装配各领域服务端口。当前 manager.Service 是单一聚合，同时满足全部端口；
	// 端口声明与实现分离后，装配点成为"谁提供什么能力"的唯一可见处。
	ports := router.Ports{
		Agent: a.svc, Conversation: a.svc, Workspace: a.svc, Runtime: a.svc,
		Capability: a.svc, Library: a.svc, Usage: a.svc, Search: a.svc, Export: a.svc,
	}
	a.srv = newHTTPServer(cfg.Addr, router.New(ports, a.bus, router.Options{Token: cfg.Token}))
	if cfg.Token != "" {
		slog.Info("serve: 已启用单操作员令牌鉴权（除 /health 外全部 /v1/forge 路由要求 Bearer token）")
	}
	return nil
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

	if err := a.srv.Start(); err != nil {
		return fatalErr("serve: %v", err)
	}
	return nil
}

// Close 释放装配的资源。顺序与改造前 defer 的 LIFO 一致：先关编排器（回收子进程），再关持久层。
func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.orch != nil {
			a.orch.Close()
		}
		if a.st != nil {
			_ = a.st.Close()
		}
	})
}
