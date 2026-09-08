package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/morehao/ark-forge/internal/infra/config"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/runtime/echo"
	"github.com/morehao/ark-forge/internal/server"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

const usageText = `forge — ark-forge 控制面 daemon（release 单二进制内嵌前端面板）

用法：
  forge serve [--addr :8099]    启动控制面（REST 会话动作 + WS /ws/events 实时通道 + 内嵌面板）
  forge demo [flags]            在指定 runtime 上跑一次真实 fresh 会话并打印事件（冒烟）
  forge migrate [flags]         transcript → SQLite 迁移（P1：幂等 + 事务整体回滚 + 数量校验）
  forge transcripts prune       transcript 目录显式清理（--older-than；ADR-5：不设自动删除）

子命令帮助：forge serve -h / forge demo -h / forge migrate -h / forge transcripts prune -h
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
	addr := fs.String("addr", "", "监听地址（默认读 FORGE_ADDR/`:8099`）")
	turnTimeout := fs.Duration("turn-timeout", 0, "单轮 turn 看门狗超时（0=关闭；默认读 FORGE_TURN_TIMEOUT）")
	_ = fs.Parse(args)

	cfg := config.Load()
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *turnTimeout > 0 {
		cfg.TurnTimeout = *turnTimeout
	}

	reg := runtime.DefaultRegistry()
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

	orch := manager.NewOrchestrator(svc, reg)
	// Agent definitions（P2）：FORGE_AGENTS 目录下 <id>.json → 注册到 agents 表（服务端无页面时也热加载）。
	svc.LoadAgentDefinitions(cfg.AgentDir)
	// 能力数据面（P1）：FORGE_CAPABILITIES 目录下 skills|mcp|tools/*.json → Skill/MCP/Tool 目录。
	svc.LoadCapabilityDefinitions(cfg.CapabilityDir)
	if mode == "sqlite" {
		// 启动恢复会话级权限记忆（permission_memories 表 → perm.mem，allow/reject 双侧）。
		if err := orch.RestoreMemoriesFromStore(context.Background()); err != nil {
			slog.Warn("serve: 恢复权限记忆失败", "err", err)
		}
	}
	if cfg.TurnTimeout > 0 {
		orch.SetTurnTimeout(cfg.TurnTimeout)
	}
	// 权限/elicitation 挂起超时（默认 30min；FORGE_PERMISSION_TIMEOUT / FORGE_ELICITATION_TIMEOUT 覆盖）。
	orch.SetPermissionTimeout(cfg.PermissionTimeout)
	orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	// 空闲回收（P2）：StartAgent 暖进程闲置超阈值自动停（FORGE_IDLE_TIMEOUT，默认 5m；0 = 关闭）。
	orch.StartIdleReaper(cfg.ReapInterval, cfg.IdleTimeout)
	defer orch.Close()

	srv := server.New(server.Config{Addr: cfg.Addr}, svc, bus)

	// 优雅退出：信号 → 停 HTTP → 关编排器（回收 runtime 子进程）。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
	}()

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
	cfg := config.Load()
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

	cfg := config.Load()
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
	cfg := config.Load()
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
