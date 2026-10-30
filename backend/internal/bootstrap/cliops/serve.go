package cliops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/morehao/ark-mate/internal/bootstrap/maintenance"

	"github.com/morehao/ark-mate/internal/bootstrap/app"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/golib/glog"
)

// serve 启动控制面：REST（列表 + sessions create/prompt/cancel）+ WS /ws/events。
// 会话动作由 Orchestrator 跑真实 ACP runtime；事件经 eventbus 出流。
//
// 本函数只做三件事：解析子命令参数、把配置交给 internal/bootstrap/app 装配、按退出码收尾。
// 依赖装配与启停生命周期全在 app 包（见其包注释的分层说明）。
func Serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "监听地址（默认读 MATE_ADDR/`127.0.0.1:10001`；对外暴露请显式指定并自担无鉴权风险）")
	runTimeout := fs.Duration("run-timeout", 0, "单轮 run 看门狗超时（0=关闭；默认读 MATE_RUN_TIMEOUT）")
	readyStdout := fs.Bool("ready-stdout", false,
		"监听成功后往 stdout 打一行 `MATE_READY {\"addr\":\"127.0.0.1:<port>\",\"port\":<port>}`（就绪行只含地址）；"+
			"桌面壳据此立刻拿到地址，不传则壳退回 health 轮询")
	webDir := fs.String("web-dir", "",
		"前端资产目录（绝对路径，须含 index.html；同 MATE_WEB_DIR）。不传则用构建期内置资产；"+
			"两者都没有 → 拒绝启动（退出码 3）。显式给了目录就**只**用它，不回落内嵌")
	assetWatch := fs.Duration("asset-watch", 0,
		"开发期资产重建通知的轮询间隔（P6-A；同 MATE_ASSET_WATCH；**默认 0 = 关闭**）。"+
			"开了以后核心会 stat-poll 自己正在服务的那份 --web-dir，内容真的变了就经 "+
			router.AssetWatchPath+"（SSE）通知页面刷新，省掉「改完再手动刷新」。只在 `make dev` 这种"+
			"开发形态下有意义：交付形态（桌面壳/容器）不传，该路由也就不存在")
	exitWithParent := fs.Bool("exit-with-parent", false,
		"父进程消失即自动退出（POSIX：父进程死后被 reparent，`Getppid` 随之变化）；"+
			"桌面壳用 `kill -9` 强杀时靠它避免留下孤儿核心与 runtime 子进程。Windows 无 reparent 语义，那里要等 Job Object（M4-3）")
	payloads := fs.String("payloads", "",
		"桌面壳交来的**已解析**随包载荷清单（JSON 数组，形如 "+
			`[{"name":"dsh","version":"0.1.7-rc.2","digest":"sha256:<hex>","entry":"/abs/lib/bin.js","source":"bundled"}]`+
			"）；name/version/digest/source 有意义，其中 source 只接受 bundled|runtime-env|path，entry 被忽略（/meta 无此字段）。"+
			"清单真源是壳（三级解析只有它做），核心只把它透出到 /meta 的诊断面；不传 = 非桌面态，payloads 为空数组。"+
			"语法错误/缺 name/source 非法/name 重复/超 16 条 → 退出码 2")
	_ = fs.Parse(args)

	cfg, sources := mustConfig()
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *runTimeout > 0 {
		cfg.RunTimeout = *runTimeout
	}
	// CLI flag 优先级最高（与 --addr 同一条规则：默认 < config.json < env < MATE_* < flag）。
	if *webDir != "" {
		cfg.WebDir = *webDir
	}
	// 同理：显式给了正值才覆盖（`--asset-watch=0` 与不传等价，都是关闭）。
	if *assetWatch > 0 {
		cfg.AssetWatch = *assetWatch
	}

	opts := []app.Option{}
	if *readyStdout {
		opts = append(opts, app.WithReadyStdout(true))
	}
	// 载荷清单（M3-4）：解析在命令层（app 只收结构化值），
	// 校验失败按"用法/配置错误"处理（退出码 2，与 --web-dir 指错目录同一档——
	// 都是"调用方把参数给错了"，而不是核心自己崩了）。
	if *payloads != "" {
		ps, err := maintenance.ParsePayloads(*payloads)
		if err != nil {
			exitWith(&assembly.ExitError{Code: 2, Err: err})
		}
		opts = append(opts, app.WithPayloads(ps))
	}

	// 优雅退出：SIGINT/SIGTERM → 停 HTTP → 关编排器（回收 runtime 子进程）。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 父进程自保（M1-6）：桌面壳被强杀时不会发 SIGTERM，核心必须自己发现"爹没了"并退出，
	// 否则它和它手里的 runtime 子进程会一起变孤儿。取消 ctx 走的是与 SIGTERM 同一条优雅停机路径。
	if *exitWithParent {
		// 壳一死，核心的 stdout/stderr 就没读者了——不忽略 SIGPIPE 的话，停机过程中的
		// 第一条日志就会把进程当场打死，优雅停机形同虚设（见 ignoreSIGPIPE 的注释）。
		maintenance.IgnoreSIGPIPE()
		go maintenance.WatchParentExit(ctx, os.Getppid, maintenance.ParentPollInterval, func() {
			glog.Warnw(context.Background(), "serve: 父进程已消失（壳被强杀？），开始优雅停机")
			cancel()
		})
	}

	a := app.New(cfg, sources, opts...)
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
	var ee *assembly.ExitError
	code := 1
	if errors.As(err, &ee) {
		code = ee.Code
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(code)
}
