package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/morehao/ark-forge/internal/infra/config"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server"
	"github.com/morehao/ark-forge/internal/server/eventbus"
)

const usageText = `forge — ark-forge 控制面 daemon（M0：打通 ACP 直连）

用法：
  forge serve [--addr :8099]    启动控制面服务（REST 列表 + WS /ws/events 实时通道）
  forge demo [flags]            在指定 runtime 上跑一次真实 fresh 会话并打印事件（M0 冒烟）

子命令帮助：forge serve -h / forge demo -h
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
	case "-h", "--help", "help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
}

// serve 启动控制面：REST（列表 + sessions create/prompt/cancel）+ WS /ws/events。
// 会话动作由 Orchestrator 跑真实 ACP runtime；事件经 eventbus 出流。M1 前哨起
// REST 可触发会话（此前仅 forge demo 可跑真实链路）。
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "监听地址（默认读 FORGE_ADDR/`:8099`）")
	_ = fs.Parse(args)

	cfg := config.Load()
	if *addr != "" {
		cfg.Addr = *addr
	}

	reg := runtime.DefaultRegistry()
	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	orch := manager.NewOrchestrator(svc, reg)
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
	runtimeID := fs.String("runtime", "", "runtime id（opencode / deepseekharness）")
	prompt := fs.String("prompt", "你好，请用一句话介绍你自己。", "发送给 agent 的 prompt")
	cwd := fs.String("cwd", "", "会话工作目录（默认当前目录）")
	configPath := fs.String("config", "", "自定义 adapter 的 {config} 占位替换（当前内置 runtime 均无占位，预留扩展）")
	allowAll := fs.Bool("allow-all", false, "放行一切权限请求（仅调试；默认拒绝）")
	timeout := fs.Duration("timeout", 5*time.Minute, "单次运行超时")
	_ = fs.Parse(args)

	if *runtimeID == "" {
		fmt.Fprintln(os.Stderr, "demo: --runtime 必填（opencode / deepseekharness）")
		os.Exit(2)
	}
	// 冒烟输出含 runtime stderr（登录提示/诊断），提到 Debug 便于排查。
	slog.SetLogLoggerLevel(slog.LevelDebug)

	reg := runtime.DefaultRegistry()
	if _, err := reg.Require(*runtimeID); err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v（可用：opencode / deepseekharness）\n", err)
		os.Exit(2)
	}

	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	orch := manager.NewOrchestrator(svc, reg)
	orch.SetAllowAll(*allowAll)
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
	fmt.Printf("✔ demo 完成 session=%s agent=%s stop_reason=%q\n", res.SessionID, res.AgentID, res.StopReason)
}

// printEvent 把 bus 事件按类型打印为人类可读行（冒烟输出）。
func printEvent(evt eventbus.Event) {
	p, _ := evt.Payload.(map[string]any)
	text, _ := p["text"].(string)
	sid, _ := p["session_id"].(string)
	switch evt.Type {
	case manager.BusSessionCreated:
		fmt.Printf("🆕 session.created  id=%s\n", sid)
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
