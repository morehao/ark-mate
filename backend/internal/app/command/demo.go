package command

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/pkg/runtime"
)

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
	cfg, _ := mustConfig()
	// runtime 配置隔离（M3-3）：demo 也会真的拉起底座子进程，必须与 serve 走同一份隔离表
	// （否则 demo 读的是用户自己的 ~/.dsh / ~/.config/opencode，与平台会话行为不一致）。
	reg.ApplyIsolationEnv(cfg.HomeDir)
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
