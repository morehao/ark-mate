package maintenance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/sessionruntime"
)

// demo 在**构建期选定的** runtime 上跑一次真实会话（M0 DoD 的可执行验证路径）。
//
// 底座不再由命令行选择（产品约束：用户不能选择 runtime）——它是二进制的属性，见
// pkg/runtime/build.go。要换底座就换构建（`make build RUNTIME=<id>`）。
func Demo(args []string) {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	prompt := fs.String("prompt", "你好，请用一句话介绍你自己。", "发送给 agent 的 prompt")
	cwd := fs.String("cwd", "", "会话工作目录（默认当前目录）")
	configPath := fs.String("config", "", "自定义 adapter 的 {config} 占位替换（当前内置 runtime 均无占位，预留扩展）")
	allowAll := fs.Bool("allow-all", false, "放行一切权限请求（仅调试；默认拒绝）")
	timeout := fs.Duration("timeout", 5*time.Minute, "单次运行超时")
	_ = fs.Parse(args)

	// 冒烟输出含 runtime stderr（登录提示/诊断），降到 Debug 便于排查。
	RaiseLogVerbosity()

	cfg, _ := config.MustLoad()
	remoteSpecs := make([]runtime.Spec, 0, len(cfg.RemoteRuntimes))
	for _, rt := range cfg.RemoteRuntimes {
		remoteSpecs = append(remoteSpecs, runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
	}
	reg, err := runtime.ProductRegistry(assembly.AllAdapters(), remoteSpecs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		os.Exit(2)
	}

	bus := eventbus.New()
	svc := sessionruntime.NewService(reg, bus)
	// runtime 配置隔离（M3-3）：demo 也会真的拉起底座子进程，必须与 serve 走同一份隔离表
	// （否则 demo 读的是用户自己的底座 home 目录，与平台会话行为不一致）。
	reg.ApplyIsolationEnv(cfg.HomeDir)
	svc.SessionPlane().SetTranscriptDir(cfg.TranscriptDir) // demo 会话也落 transcript（wire 事实源）
	orch := sessionruntime.NewOrchestrator(svc, reg)
	orch.SetAllowAll(*allowAll)
	orch.SetPermissionTimeout(cfg.PermissionTimeout)
	orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	defer orch.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// 订阅 bus（回放全部历史 + live），边跑边打印域事件帧。
	// `SubscribeAfter(nil)` = 每个聚合都从 -1（最早一条）起回放：demo 要看的正是**全部**事件，
	// 而"所有聚合"这件事在 v21 之后写不成一个 int64（位次是会话内的量）。
	sub := bus.SubscribeAfter(nil)
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

	fmt.Printf("▶ demo runtime=%s prompt=%q\n", runtime.SelectedRuntime(), *prompt)
	res, err := orch.RunOnce(ctx, sessionruntime.RunOptions{
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
	fmt.Printf("✔ demo 完成 session=%s runtime=%s stop_reason=%q\n", res.SessionID, res.Runtime, res.StopReason)
}

// printEvent 把 bus 事件按类型打印为人类可读行（冒烟输出）。
func printEvent(evt eventbus.Event) {
	p, _ := evt.Payload.(map[string]any)
	text, _ := p["text"].(string)
	sid, _ := p["session_id"].(string)
	switch evt.Type {
	case session.BusSessionCreated:
		fmt.Printf("🆕 session.created  id=%s\n", sid)
	case session.BusMessage:
		if text != "" {
			fmt.Print(text)
		}
	case session.BusThought:
		if text != "" {
			fmt.Printf("\n💭 %s\n", text)
		}
	case session.BusRunStarted:
		runID, _ := p["run_id"].(string)
		fmt.Printf("\n▶ run.started  run_id=%s\n", runID)
	case session.BusUserMessage:
		fmt.Printf("\n👤 %s\n", text)
	case session.BusToolCall, session.BusToolUpdate:
		title, _ := p["title"].(string)
		status, _ := p["status"].(string)
		fmt.Printf("\n🛠 %s %s [%s]\n", evt.Type, title, status)
	case session.BusRunEnded:
		stop, _ := p["stop_reason"].(string)
		fmt.Printf("\n✅ run.ended  stop_reason=%q\n", stop)
	case session.BusRunError:
		code, _ := p["code"].(string)
		cls, _ := p["class"].(string)
		fmt.Printf("\n❌ run.error  code=%s class=%s\n", code, cls)
	case session.BusRunCancelled:
		fmt.Printf("\n⛔ run.cancelled\n")
	}
}
