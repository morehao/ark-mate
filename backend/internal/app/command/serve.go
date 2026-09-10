package command

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/server"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/pkg/config"
	"github.com/morehao/ark-forge/pkg/procreg"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// serve 启动控制面：REST（列表 + conversations create/prompt/cancel）+ WS /ws/events。
// 会话动作由 Orchestrator 跑真实 ACP runtime；事件经 eventbus 出流。M1 前哨起
// REST 可触发会话（此前仅 forge demo 可跑真实链路）。
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "监听地址（默认读 FORGE_ADDR/`127.0.0.1:10001`；对外暴露请显式指定并自担无鉴权风险）")
	turnTimeout := fs.Duration("turn-timeout", 0, "单轮 turn 看门狗超时（0=关闭；默认读 FORGE_TURN_TIMEOUT）")
	_ = fs.Parse(args)

	cfg, sources := mustConfig()
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *turnTimeout > 0 {
		cfg.TurnTimeout = *turnTimeout
	}

	reg := runtime.DefaultRegistry()
	// 远程 runtime（A5）：配置目录 runtimes.json 声明的远程 ACP 端点注册进同一注册表，
	// agent 的 runtimes 字段即可引用；与内置 id 冲突时启动即失败（不静默覆盖内置底座）。
	for _, rt := range cfg.RemoteRuntimes {
		if _, exists := reg.Get(rt.ID); exists {
			fmt.Fprintf(os.Stderr, "serve: runtimes.json 的 id %q 与内置 runtime 冲突\n", rt.ID)
			os.Exit(2)
		}
		reg.Register(runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
		slog.Info("serve: 注册远程 runtime", "id", rt.ID, "transport", rt.Transport, "endpoint", rt.Endpoint)
	}
	bus := eventbus.New()
	svc := manager.NewService(reg, bus)

	// 持久层装配（P1）：FORGE_STORE=sqlite → SQLite（DB 为事实源，transcript 停写保留只读）；
	// =memory → 维持 transcript 落盘 + 启动回读；auto（默认）→ DB 文件已存在则 sqlite，否则 memory。
	mode := cfg.StoreMode
	if mode == "" {
		mode = "memory"
		if fi, err := os.Stat(cfg.DBPath); err == nil && fi.Size() > 0 {
			mode = "sqlite"
		}
	}
	switch mode {
	case "sqlite":
		// 配置目录化后 DB 路径可由文件指定，父目录未必存在：先补齐（此前只依赖用户手建目录）。
		if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "serve: 创建 DB 目录 %s: %v\n", dir, err)
				os.Exit(1)
			}
		}
		st, err := store.Open(cfg.DBPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: 打开持久层 %s: %v\n", cfg.DBPath, err)
			os.Exit(1)
		}
		defer st.Close()
		svc.EnableStore(st)
		conversations, events, rErr := svc.RestoreFromStore(context.Background())
		if rErr != nil {
			slog.Warn("serve: 从持久层恢复失败", "err", rErr)
		}
		// transcript 未迁移提示：DB 为空但 transcript 目录有文件 → 提示先 `forge migrate`。
		if conversations == 0 && events == 0 {
			if files, gErr := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl")); gErr == nil && len(files) > 0 {
				fmt.Fprintf(os.Stderr, "serve: %s 下仍有 %d 个 transcript 未导入；运行 `forge migrate` 后重启即可在 sqlite 模式下看到旧会话\n", cfg.TranscriptDir, len(files))
			}
		}
	case "memory", "":
		svc.SetTranscriptDir(cfg.TranscriptDir) // 会话 wire transcript（M1 #5，~/.forge/transcripts 或 FORGE_TRANSCRIPTS）
	default:
		fmt.Fprintf(os.Stderr, "serve: 未知 FORGE_STORE=%q（sqlite|memory）\n", mode)
		os.Exit(2)
	}
	// 工作空间策略：未指定 cwd 的建会话请求在 <workspace_dir>/<会话id>/ 下代建临时空间
	// （空 = 关闭代建，此时未指定即报 workspace_required，不回落 serve 进程目录）；
	// workspace_roots 非空时把显式 cwd 与目录浏览收在白名单内，代建根**自动并入**生效名单
	// （平台自己的目录本就该天然合法，不再要求运维成对配置）。
	if err := svc.SetWorkspacePolicy(cfg.WorkspaceDir, cfg.WorkspaceRoots); err != nil {
		fmt.Fprintf(os.Stderr, "serve: 工作空间策略非法: %v\n", err)
		os.Exit(2)
	}
	// 白名单的持久化端口：面板 PUT /settings/workspace 经它写回配置目录（config.json）。
	// 传入该键的**最终来源**：来源高于配置文件（进程 env / env 文件 / flag）时端口会报"锁定"，
	// 面板随之转只读——这比"面板显示保存成功、重启后被 env 覆盖"诚实。
	svc.SetWorkspaceSettingsStore(config.NewSettingsStore(cfg.ConfigDir, sources["workspace_roots"]))

	orch := manager.NewOrchestrator(svc, reg)
	// 进程注册表（M3 v4③）：登记本 daemon 拉起的 runtime 子进程；启动时回收上一实例遗留的
	// 孤儿进程（daemon 被 SIGKILL/崩溃时子进程因独立进程组而存活）。
	if p := cfg.ProcRegistryPath; p != "" {
		reg2, prErr := procreg.Open(p)
		if prErr != nil {
			slog.Warn("serve: 打开进程注册表失败（孤儿回收已禁用）", "path", p, "err", prErr)
		} else {
			orch.SetProcRegistry(reg2)
			if rep, rErr := orch.ReapOrphans(cfg.ReapGrace); rErr != nil {
				slog.Warn("serve: 孤儿进程回收失败", "err", rErr)
			} else if rep.Total() > 0 {
				slog.Info("serve: 孤儿进程回收", "reaped", len(rep.Reaped),
					"cleaned", len(rep.Cleaned), "skipped", len(rep.Skipped), "registry", p)
			}
		}
	}
	// Agent definitions（P2）：FORGE_AGENTS 目录下 <id>.json → 注册到 agents 表（服务端无页面时也热加载）。
	svc.LoadAgentDefinitions(cfg.AgentDir)
	// 能力数据面（P1）：FORGE_CAPABILITIES 目录下 skills|mcp|tools/*.json → Skill/MCP/Tool 目录。
	svc.LoadCapabilityDefinitions(cfg.CapabilityDir)
	// skill 物化（§16.5 切片 D）：FORGE_SKILLS_MATERIALIZE_DIR 显式开启时，把 opencode
	// SKILL.md 源 skill 与目标目录重校（物化缺失/更新 + 删除联动，只删 forge 标记目录）；
	// 默认关闭（不写用户目录）。
	if dir := cfg.SkillMaterializeDir; dir != "" {
		if w, r, mErr := svc.ReconcileSkills(dir); mErr != nil {
			slog.Warn("serve: skill 物化重校失败", "dir", dir, "err", mErr)
		} else if w > 0 || r > 0 {
			slog.Info("serve: skill 物化重校完成", "dir", dir, "written", w, "removed", r)
		}
	}
	// C4 多 root 投影：按 runtime 各写自己的根目录（只物化声明该 runtime 的 skill）。
	// 排序遍历保证日志与执行顺序稳定（map 迭代无序）。
	if len(cfg.SkillMaterializeDirs) > 0 {
		runtimes := make([]string, 0, len(cfg.SkillMaterializeDirs))
		for rt := range cfg.SkillMaterializeDirs {
			runtimes = append(runtimes, rt)
		}
		sort.Strings(runtimes)
		for _, rt := range runtimes {
			dir := cfg.SkillMaterializeDirs[rt]
			w, r, mErr := svc.ReconcileSkillsFor(dir, rt)
			if mErr != nil {
				slog.Warn("serve: skill 物化重校失败", "dir", dir, "runtime", rt, "err", mErr)
				continue
			}
			if w > 0 || r > 0 {
				slog.Info("serve: skill 物化重校完成", "dir", dir, "runtime", rt, "written", w, "removed", r)
			}
		}
	}
	if mode == "sqlite" {
		// 启动恢复会话级权限记忆（permission_memories 表 → perm.mem，allow/reject 双侧）。
		if err := orch.RestoreMemoriesFromStore(context.Background()); err != nil {
			slog.Warn("serve: 恢复权限记忆失败", "err", err)
		}
	}
	if cfg.TurnTimeout > 0 {
		orch.SetTurnTimeout(cfg.TurnTimeout)
	}
	// handoff memo 字符预算（P3 F3；FORGE_HANDOFF_MAX_CHARS 覆盖，默认 6000）。
	orch.SetHandoffMaxChars(cfg.HandoffMaxChars)
	// 权限/elicitation 挂起超时（默认 30min；FORGE_PERMISSION_TIMEOUT / FORGE_ELICITATION_TIMEOUT 覆盖）。
	orch.SetPermissionTimeout(cfg.PermissionTimeout)
	orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	// 原生权限映射（B5）：FORGE_NATIVE_PERMISSION=off 时关闭（默认 auto）。
	if cfg.NativePermission == config.NativePermissionOff {
		svc.SetNativePermission(false)
		slog.Info("serve: 原生权限映射已关闭（FORGE_NATIVE_PERMISSION=off）")
	}
	// 空闲回收（P2）：StartAgent 暖进程闲置超阈值自动停（FORGE_IDLE_TIMEOUT，默认 5m；0 = 关闭）。
	orch.StartIdleReaper(cfg.ReapInterval, cfg.IdleTimeout)
	defer orch.Close()

	srv := server.New(server.Config{Addr: cfg.Addr, Token: cfg.Token}, svc, bus)
	if cfg.Token != "" {
		slog.Info("serve: 已启用单操作员令牌鉴权（除 /health 外全部 /v1/forge 路由要求 Bearer token）")
	}

	// 优雅退出：信号 → 停 HTTP → 关编排器（回收 runtime 子进程）。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
	}()

	// MCP 自动探测（B4；FORGE_MCP_AUTO_PROBE=startup，默认 off）：HTTP 起来后后台逐个探测，
	// 不阻塞服务启动；结果与显式 POST /mcps/:id/probe 一致（写 Tool 目录 + 探测状态）。
	if cfg.McpAutoProbe == config.McpAutoProbeStartup {
		go func() {
			slog.Info("serve: MCP 自动探测已启用（startup 模式，后台逐个探测已注册 server）")
			_ = svc.AutoProbeMcpServers(ctx)
		}()
	}

	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}
