package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/morehao/ark-forge/internal/app"
)

// serve 启动控制面：REST（列表 + conversations create/prompt/cancel）+ WS /ws/events。
// 会话动作由 Orchestrator 跑真实 ACP runtime；事件经 eventbus 出流。
//
// 本函数只做三件事：解析子命令参数、把配置交给 internal/app 装配、按退出码收尾。
// 依赖装配与启停生命周期全在 app 包（见其包注释的分层说明）。
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

	// 优雅退出：SIGINT/SIGTERM → 停 HTTP → 关编排器（回收 runtime 子进程）。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	a := app.New(cfg, sources)
	if err := a.Init(ctx); err != nil {
		exitWith(err)
	}
	defer a.Close()

	if err := a.Run(ctx); err != nil {
		exitWith(err)
	}
}

// exitWith 按装配错误自带的退出码收尾（配置类 2、运行期 1），未标注的按 1 处理。
func exitWith(err error) {
	var ee *app.ExitError
	code := 1
	if errors.As(err, &ee) {
		code = ee.Code
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(code)
}
