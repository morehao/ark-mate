package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/infra/config"
	"github.com/morehao/ark-forge/internal/infra/procreg"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/runtime/echo"
	"github.com/morehao/ark-forge/internal/server"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

// mustConfig 解析配置（含 .forge/config 目录化）。配置文件非法时打印明确错误并退出 2
// （配置错误必须在启动阶段暴露，而不是静默回落默认值）。
//
// 连 Sources 一起返回：来源不只是排障信息，还是**权限事实**——被进程 env/flag 锁定的配置键，
// 面板写入必然被覆盖，serve 据此把它转成"只读设置"（见 config.NewSettingsStore）。
func mustConfig() (config.Config, config.Sources) {
	cfg, sources, err := config.LoadResolved("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n\n配置目录: %s（FORGE_CONFIG_DIR 可改）\n", err, config.ConfigDirFromEnv())
		os.Exit(2)
	}
	return cfg, sources
}

// configCmd 打印解析后的配置与每项来源（排障/上线核对用；不启动任何进程）。
func configCmd(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出（含每项来源）")
	dir := fs.String("config-dir", "", "配置目录（默认 FORGE_CONFIG_DIR 或 ~/.forge/config）")
	_ = fs.Parse(args)

	cfg, sources, err := config.LoadResolved(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	if *asJSON {
		doc := map[string]any{
			"config_dir": cfg.ConfigDir,
			"config": map[string]any{
				"addr":                    cfg.Addr,
				"transcripts":             cfg.TranscriptDir,
				"db":                      cfg.DBPath,
				"store":                   cfg.StoreMode,
				"agents":                  cfg.AgentDir,
				"capabilities":            cfg.CapabilityDir,
				"workspace_dir":           cfg.WorkspaceDir,
				"workspace_roots":         strings.Join(cfg.WorkspaceRoots, ","),
				"skills_materialize_dir":  cfg.SkillMaterializeDir,
				"skills_materialize_dirs": formatMaterializeDirs(cfg.SkillMaterializeDirs),
				"idle_timeout":            cfg.IdleTimeout.String(),
				"reap_interval":           cfg.ReapInterval.String(),
				"turn_timeout":            cfg.TurnTimeout.String(),
				"handoff_max_chars":       cfg.HandoffMaxChars,
				"permission_timeout":      cfg.PermissionTimeout.String(),
				"elicitation_timeout":     cfg.ElicitationTimeout.String(),
				"proc_registry":           cfg.ProcRegistryPath,
				"reap_grace":              cfg.ReapGrace.String(),
				"token":                   maskToken(cfg.Token),
				"mcp_auto_probe":          cfg.McpAutoProbe,
				"native_permission":       cfg.NativePermission,
			},
			"remote_runtimes": cfg.RemoteRuntimes,
			"sources":         sources,
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	src := func(k string) string {
		if s, ok := sources[k]; ok {
			return string(s)
		}
		return string(config.SourceDefault)
	}
	fmt.Printf("配置目录: %s\n", cfg.ConfigDir)
	fmt.Printf("  %-22s %-38s [%s]\n", "addr", cfg.Addr, src("addr"))
	fmt.Printf("  %-22s %-38s [%s]\n", "transcripts", cfg.TranscriptDir, src("transcripts"))
	fmt.Printf("  %-22s %-38s [%s]\n", "db", cfg.DBPath, src("db"))
	fmt.Printf("  %-22s %-38s [%s]\n", "store", storeModeLabel(cfg.StoreMode), src("store"))
	fmt.Printf("  %-22s %-38s [%s]\n", "agents", cfg.AgentDir, src("agents"))
	fmt.Printf("  %-22s %-38s [%s]\n", "capabilities", cfg.CapabilityDir, src("capabilities"))
	// 空 = 关闭代建（未指定 cwd 的建会话请求会报 workspace_required），故用 emptyLabel 明确显示。
	fmt.Printf("  %-22s %-38s [%s]\n", "workspace_dir", emptyLabel(cfg.WorkspaceDir), src("workspace_dir"))
	// 空 = 不限制（任何可读目录都能当工作空间）；非空 = 显式 cwd 与目录浏览都被收在根内，
	// 且 workspace_dir 会**隐式并入**生效名单（无需手写进这里）。
	fmt.Printf("  %-22s %-38s [%s]\n", "workspace_roots", emptyLabel(strings.Join(cfg.WorkspaceRoots, ",")), src("workspace_roots"))
	// 两行注记挂在同一键下面（空标签保持列对齐）：中文标签宽度不一，自己缩进排版必然对不齐。
	//
	// 为什么要显式打印"另含代建根"：运维只配了一个根，却会在浏览面的 roots 里看到第二个——
	// 不解释就等于让人怀疑配置没生效（旧版本这里反而是启动失败，更容易被误读成配错）。
	if len(cfg.WorkspaceRoots) > 0 && cfg.WorkspaceDir != "" {
		fmt.Printf("  %-22s %-38s [隐含]\n", "", "↑ 生效名单另含平台代建根 "+cfg.WorkspaceDir)
	}
	// 被更高优先级来源锁定时，面板的设置面会**拒绝写**（只读）：排障时要能一眼看出原因。
	if s, ok := sources["workspace_roots"]; ok && s != config.SourceFile && s != config.SourceDefault {
		fmt.Printf("  %-22s %-38s [%s]\n", "", "↑ 面板设置只读（由该来源锁定）", string(s))
	}
	fmt.Printf("  %-22s %-38s [%s]\n", "skills_materialize_dir", emptyLabel(cfg.SkillMaterializeDir), src("skills_materialize_dir"))
	fmt.Printf("  %-22s %-38s [%s]\n", "skills_materialize_dirs", emptyLabel(formatMaterializeDirs(cfg.SkillMaterializeDirs)), src("skills_materialize_dirs"))
	fmt.Printf("  %-22s %-38s [%s]\n", "idle_timeout", cfg.IdleTimeout.String(), src("idle_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "reap_interval", cfg.ReapInterval.String(), src("reap_interval"))
	fmt.Printf("  %-22s %-38s [%s]\n", "turn_timeout", cfg.TurnTimeout.String(), src("turn_timeout"))
	fmt.Printf("  %-22s %-38d [%s]\n", "handoff_max_chars", cfg.HandoffMaxChars, src("handoff_max_chars"))
	fmt.Printf("  %-22s %-38s [%s]\n", "permission_timeout", cfg.PermissionTimeout.String(), src("permission_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "elicitation_timeout", cfg.ElicitationTimeout.String(), src("elicitation_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "proc_registry", emptyLabel(cfg.ProcRegistryPath), src("proc_registry"))
	fmt.Printf("  %-22s %-38s [%s]\n", "reap_grace", cfg.ReapGrace.String(), src("reap_grace"))
	fmt.Printf("  %-22s %-38s [%s]\n", "token", maskToken(cfg.Token), src("token"))
	fmt.Printf("  %-22s %-38s [%s]\n", "mcp_auto_probe", emptyLabel(cfg.McpAutoProbe), src("mcp_auto_probe"))
	fmt.Printf("  %-22s %-38s [%s]\n", "native_permission", emptyLabel(cfg.NativePermission), src("native_permission"))
	if len(cfg.RemoteRuntimes) == 0 {
		fmt.Printf("  %-22s %-38s [%s]\n", "remote_runtimes", "(无)", string(config.SourceDefault))
	}
	for _, rt := range cfg.RemoteRuntimes {
		fmt.Printf("  %-22s %-38s [%s]\n", "remote_runtime:"+rt.ID, rt.Transport+" "+rt.Endpoint, string(config.SourceFile))
	}
}

// maskToken 令牌只显示长度与首尾各 2 字符（避免配置核对时把令牌打进日志/截图）。
func maskToken(t string) string {
	if t == "" {
		return "(未设置，不鉴权)"
	}
	if len(t) <= 8 {
		return fmt.Sprintf("(已设置，%d 字符)", len(t))
	}
	return fmt.Sprintf("%s…%s（%d 字符）", t[:2], t[len(t)-2:], len(t))
}

func storeModeLabel(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

func emptyLabel(v string) string {
	if v == "" {
		return "(空)"
	}
	return v
}

// acpServe 把内置 echo ACP 底座暴露成远程端点（A5）：
// 本机/跨机的另一个 ark-forge 可以在 .forge/config/runtimes.json 里把它声明为远程 runtime，
// 从而在不装 opencode/dsh、不用模型 key 的前提下演示/验证远程 ACP 链路。
func acpServe(args []string) {
	fs := flag.NewFlagSet("acp-serve", flag.ExitOnError)
	transport := fs.String("transport", "ws", "远程传输形态：ws（WebSocket）| http（Streamable HTTP）")
	listen := fs.String("listen", "127.0.0.1:10020", "监听地址（默认仅回环）")
	path := fs.String("path", "/acp", "端点路径")
	_ = fs.Parse(args)

	opts := acl.RemoteServerOptions{
		Serve:           func(in io.Writer, out io.Reader) { echo.Serve(in, out) },
		SessionIDPrefix: "echo",
		Logger:          slog.Default(),
	}
	mux := http.NewServeMux()
	switch strings.ToLower(strings.TrimSpace(*transport)) {
	case "ws", "websocket":
		mux.HandleFunc(*path, func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			_ = acl.ServeWSSession(ctx, conn, opts)
		})
	case "http", "streamable_http", "streamable-http":
		mux.Handle(*path, acl.NewRemoteHandler(opts))
	default:
		fmt.Fprintf(os.Stderr, "acp-serve: 不支持的 transport %q（ws|http）\n", *transport)
		os.Exit(2)
	}

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("acp-serve: echo ACP 底座已暴露为远程端点", "transport", *transport, "listen", *listen, "path", *path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "acp-serve: %v\n", err)
		os.Exit(1)
	}
}

const usageText = `forge — ark-forge 控制面 daemon（release 单二进制内嵌前端面板）

用法：
  forge serve [--addr 127.0.0.1:10001]
                                启动控制面（REST 会话动作 + WS /ws/events 实时通道 + 内嵌面板）
  forge demo [flags]            在指定 runtime 上跑一次真实 fresh 会话并打印事件（冒烟）
  forge migrate [flags]         transcript → SQLite 迁移（P1：幂等 + 事务整体回滚 + 数量校验）
  forge transcripts prune       transcript 目录显式清理（--older-than；ADR-5：不设自动删除）
  forge config [--json]         打印解析后的配置与每项来源（配置目录 .forge/config 目录化）
  forge acp-serve [--transport ws|http] [--listen host:port] [--path /acp]
                                把内置 echo ACP 底座暴露成远程端点（A5 远程传输的服务端半场）

子命令帮助：forge serve -h / forge demo -h / forge migrate -h / forge transcripts prune -h / forge config -h / forge acp-serve -h
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "demo":
		demo(os.Args[2:])
	case "migrate":
		migrate(os.Args[2:])
	case "transcripts":
		if len(os.Args) < 3 || os.Args[2] != "prune" {
			fmt.Fprintln(os.Stderr, "transcripts 用法：forge transcripts prune --older-than <duration> [--dir <dir>]")
			os.Exit(2)
		}
		transcriptsPrune(os.Args[3:])
	case "config":
		configCmd(os.Args[2:])
	case "acp-serve":
		acpServe(os.Args[2:])
	case "acp-demo":
		// echo 演示底座的 ACP server 子进程（由 serve 经 stdio 拉起，勿手动运行）。
		if err := echo.RunServer(); err != nil {
			fmt.Fprintf(os.Stderr, "acp-demo: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
}

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

// demo 在指定 runtime 上跑一次真实会话（M0 DoD 的可执行验证路径）。
func demo(args []string) {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	runtimeID := fs.String("runtime", "", "runtime id（opencode / deepseekharness / echo）")
	prompt := fs.String("prompt", "你好，请用一句话介绍你自己。", "发送给 agent 的 prompt")
	cwd := fs.String("cwd", "", "会话工作目录（默认当前目录）")
	configPath := fs.String("config", "", "自定义 adapter 的 {config} 占位替换（当前内置 runtime 均无占位，预留扩展）")
	allowAll := fs.Bool("allow-all", false, "放行一切权限请求（仅调试；默认拒绝）")
	timeout := fs.Duration("timeout", 5*time.Minute, "单次运行超时")
	_ = fs.Parse(args)

	if *runtimeID == "" {
		fmt.Fprintln(os.Stderr, "demo: --runtime 必填（opencode / deepseekharness / echo）")
		os.Exit(2)
	}
	// 冒烟输出含 runtime stderr（登录提示/诊断），提到 Debug 便于排查。
	slog.SetLogLoggerLevel(slog.LevelDebug)

	reg := runtime.DefaultRegistry()
	if _, err := reg.Require(*runtimeID); err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v（可用：opencode / deepseekharness / echo）\n", err)
		os.Exit(2)
	}

	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	cfg, _ := mustConfig()
	svc.SetTranscriptDir(cfg.TranscriptDir) // demo 会话也落 transcript（wire 事实源）
	orch := manager.NewOrchestrator(svc, reg)
	orch.SetAllowAll(*allowAll)
	orch.SetPermissionTimeout(cfg.PermissionTimeout)
	orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	defer orch.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// 订阅 bus（after=0 → 回放 + live），边跑边打印域事件帧。
	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case evt := <-sub.C:
				printEvent(evt)
			}
		}
	}()

	fmt.Printf("▶ demo runtime=%s prompt=%q\n", *runtimeID, *prompt)
	res, err := orch.RunOnce(ctx, manager.RunOptions{
		RuntimeID:  *runtimeID,
		Prompt:     *prompt,
		Cwd:        *cwd,
		ConfigPath: *configPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: 运行失败: %v\n", err)
		os.Exit(1)
	}
	// 让已入队的尾部事件被打印协程消费后退出。
	time.Sleep(300 * time.Millisecond)
	fmt.Printf("✔ demo 完成 conversation=%s agent=%s stop_reason=%q\n", res.ConversationID, res.AgentID, res.StopReason)
}

// migrate 把 transcript 目录（默认 ~/.forge/transcripts）全量导入 SQLite（P1 迁移关键路径）：
//   - 解析全部 *.jsonl（复用 manager 复读器；坏文件告警跳过）；
//   - 单事务导入（store.ImportBatch：全局 seq 重分配 → events → conversations → 数量校验 → 导入标记）；
//   - 幂等：同目录指纹已导入过则跳过；任一步失败整体回滚（原 transcript 目录不动）。
func migrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 库路径（默认 FORGE_DB 或 ~/.forge/forge.db）")
	transcriptsDir := fs.String("transcripts", "", "transcript 目录（默认 FORGE_TRANSCRIPTS 或 ~/.forge/transcripts）")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *transcriptsDir != "" {
		cfg.TranscriptDir = *transcriptsDir
	}

	files, err := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 扫描 transcript 目录: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Printf("migrate: %s 下无 .jsonl transcript，无事可做\n", cfg.TranscriptDir)
		return
	}
	marker := fingerprintTranscripts(files)

	items := make([]store.ImportItem, 0, len(files))
	skipped, lineCount := 0, 0
	for _, path := range files {
		data, ok := manager.ReadTranscriptFile(path)
		if !ok {
			fmt.Printf("migrate: 跳过坏文件 %s\n", filepath.Base(path))
			skipped++
			continue
		}
		events := make([]store.Event, 0, len(data.Lines))
		for _, l := range data.Lines {
			b, mErr := json.Marshal(l.Payload)
			if mErr != nil {
				continue
			}
			events = append(events, store.Event{Type: l.Type, PayloadJSON: string(b)})
		}
		lineCount += len(events)
		items = append(items, store.ImportItem{
			Conversation: store.Conversation{
				ID:          data.SessionID,
				AgentID:     data.AgentID,
				RuntimeHint: data.RuntimeHint,
				Title:       data.Title,
				Status:      data.Status,
				CreatedAt:   data.CreatedAt,
				UpdatedAt:   data.UpdatedAt,
			},
			Events: events,
		})
	}
	if len(items) == 0 {
		fmt.Println("migrate: 无可用 transcript，退出")
		return
	}

	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: 创建库目录: %v\n", err)
			os.Exit(1)
		}
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 打开库 %s: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	ev, sf, err := st.ImportBatch(context.Background(), items, marker, len(items), lineCount)
	if errors.Is(err, store.ErrAlreadyImported) {
		fmt.Printf("migrate: %s 已导入过该目录（幂等跳过）\n", cfg.DBPath)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 导入失败（已整体回滚，原 transcript 目录未动）: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✔ migrate: %d 会话 / %d 事件 已导入 → %s（跳过坏文件 %d）\n", sf, ev, cfg.DBPath, skipped)
}

// transcriptPrune 显式清理 transcript 目录中超过 --older-than 的文件（ADR-5：
// transcript 为审计/回滚保留，不设自动删除；清理仅经本命令，默认目录 FORGE_TRANSCRIPTS）。
func transcriptsPrune(args []string) {
	fs := flag.NewFlagSet("transcripts prune", flag.ExitOnError)
	olderThan := fs.String("older-than", "", "删除修改时间早于此时长的 *.jsonl（必填；支持 30d / 168h / 72h30m 等）")
	dir := fs.String("dir", "", "transcript 目录（默认 FORGE_TRANSCRIPTS 或 ~/.forge/transcripts）")
	_ = fs.Parse(args)
	cfg, _ := mustConfig()
	if *dir != "" {
		cfg.TranscriptDir = *dir
	}
	age, err := parseAge(*olderThan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "transcripts prune: --older-than 非法: %v（示例 30d / 168h）\n", err)
		os.Exit(2)
	}
	files, err := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "transcripts prune: %v\n", err)
		os.Exit(1)
	}
	cutoff := time.Now().Add(-age)
	deleted, kept := 0, 0
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			if err := os.Remove(p); err != nil {
				fmt.Fprintf(os.Stderr, "transcripts prune: 删除 %s: %v\n", filepath.Base(p), err)
				continue
			}
			deleted++
			continue
		}
		kept++
	}
	fmt.Printf("✔ transcripts prune: 删除 %d（>%s）、保留 %d（%s）\n", deleted, age.String(), kept, cfg.TranscriptDir)
}

// parseAge 解析清理时长：支持 Go duration（如 168h/72h30m）与天（30d/7d 等）；空/非法报错。
func parseAge(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("空值")
	}
	if len(s) > 1 && (s[len(s)-1] == 'd' || s[len(s)-1] == 'D') {
		days, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("非法天数 %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("非法时长 %q", s)
	}
	return d, nil
}

// fingerprintTranscripts 目录指纹（文件名+大小，稳定序）——导入幂等标记。
func fingerprintTranscripts(files []string) string {
	h := sha256.New()
	for _, p := range files {
		if st, err := os.Stat(p); err == nil {
			_, _ = fmt.Fprintf(h, "%s:%d\n", filepath.Base(p), st.Size())
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// printEvent 把 bus 事件按类型打印为人类可读行（冒烟输出）。
func printEvent(evt eventbus.Event) {
	p, _ := evt.Payload.(map[string]any)
	text, _ := p["text"].(string)
	sid, _ := p["session_id"].(string)
	switch evt.Type {
	case manager.BusSessionCreated:
		fmt.Printf("🆕 conversation.created  id=%s\n", sid)
	case manager.BusMessage:
		if text != "" {
			fmt.Print(text)
		}
	case manager.BusThought:
		if text != "" {
			fmt.Printf("\n💭 %s\n", text)
		}
	case manager.BusUserMessage:
		fmt.Printf("\n👤 %s\n", text)
	case manager.BusToolCall, manager.BusToolUpdate:
		title, _ := p["title"].(string)
		status, _ := p["status"].(string)
		fmt.Printf("\n🛠 %s %s [%s]\n", evt.Type, title, status)
	case manager.BusTurnEnded:
		stop, _ := p["stop_reason"].(string)
		fmt.Printf("\n✅ turn.ended  stop_reason=%q\n", stop)
	case manager.BusTurnError:
		code, _ := p["code"].(string)
		cls, _ := p["class"].(string)
		fmt.Printf("\n❌ turn.error  code=%s class=%s\n", code, cls)
	case manager.BusRunCancelled:
		fmt.Printf("\n⛔ run.cancelled\n")
	}
}

// formatMaterializeDirs 把 runtime→dir 映射渲染为稳定的 `runtime=dir,…`（config 命令输出用）。
func formatMaterializeDirs(dirs map[string]string) string {
	if len(dirs) == 0 {
		return ""
	}
	runtimes := make([]string, 0, len(dirs))
	for rt := range dirs {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	parts := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		parts = append(parts, rt+"="+dirs[rt])
	}
	return strings.Join(parts, ",")
}
