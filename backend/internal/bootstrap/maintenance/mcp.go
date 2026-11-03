package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/drivers/gateway"
	"github.com/morehao/golib/glog"
)

// mcpCmd 分发 `mate mcp <动作>`（M7-1：网关子命令）。
func MCP(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, mcpUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "bridge":
		mcpBridge(args[1:])
	case "material":
		materialMcp(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 mcp 动作 %q\n\n%s", args[0], mcpUsage)
		os.Exit(2)
	}
}

const mcpUsage = `mcp 用法：
  mate mcp bridge --runtime <runtimeID> [--mcps '<JSON 数组>']
      平台侧 MCP stdio 网关：以 stdio 上的 MCP 服务端形态暴露该 scope 下的远程 MCP 工具
      （底座经 ACP 的 mcpServers 把这条命令拉起来；参数只带 scope 与**会话钉住的白名单**，
      不带 server 定义集——后者会让增删一个 server 就换代际。
      v19 起 scope 里没有 agent：会话不再有 agent 身份，底座由构建期固定）
      协议流走 stdout，**一切诊断走 stderr**（stdout 混进一行日志就会毁掉整条会话）
      凭据 cwd（<cwd>/.env 那一层）由核心经 env MATE_MCP_CRED_CWD 传入；没有它时用网关
      自己的 cwd 并 WARN（网关的 cwd 是底座给的，可能与核心不同）
  mate mcp material
      资料库文件级 MCP stdio 服务端（library_list_nodes / library_node_info / library_upload；
      无 search——本期只做资料库、不做知识库）。详见 mate mcp material -h
`

// mcpBridge `mate mcp bridge`：平台侧 MCP stdio 网关的进程入口。
//
// 三条纪律（都不是风格问题）：
//  1. **stdout 只走协议**（C10）：日志 handler 显式绑 stderr，不用 fmt.Print 写任何诊断；
//     连用法/错误都走 stderr —— 底座把 stdout 当 JSON-RPC 流读。
//  2. **参数解析与注入端同源**（P-2）：用 `protocol.ParseScopeArgs` 解 `mcp.GatewayBridgeArgs`
//     拼出来的那串。未知参数**当场失败**——静默忽略会表现成"网关起来了但 scope 不对"，
//     而 scope 不对的症状是"工具面不对"，最难查的一类。
//  3. **配置只用来定位数据面**（M7-2）：网关要自己打开 core 那份库（`MATE_DB` /
//     `~/.ark-mate/mate.db`），所以它按**与核心进程同一个解析器**（`mustConfig` → config
//     优先级链）取路径。这不是"第二条事实源"：取值来源与核心逐条相同（env / 配置文件 /
//     默认 home），且 `serve` 没有可让两者分叉的 CLI flag（既没有 `--db` 也没有 `--config`，
//     见 `serve` 的 flag 集）。将来若给 `serve` 加这类 flag，必须同步把取值交给网关
//     （否则网关会去读默认库，症状是"工具面恒空"）。
func mcpBridge(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, mcpUsage)
		return
	}
	scope, err := protocol.ParseScopeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp bridge: %v\n\n%s", err, mcpUsage)
		os.Exit(2)
	}

	// 父进程自保（M7-5）。底座是本进程的**直接父进程**，常态下它一死我们的 stdin 就被关闭 →
	// `Serve` 读到 EOF → 正常退出（首要机制，见函数尾的 switch）。但 EOF 不够覆盖两种情形：
	// ① 底座被强杀时写端可能仍被别处持着（管道被继承），EOF 不会到达；② stdin 上正阻塞着一次
	// 读写时退出会被拖住。故补一条 ppid 轮询兜底——与 `serve --exit-with-parent` 同一个
	// `WatchParentExit`，语义也一致（POSIX 下父进程一死会被 reparent，`os.Getppid()` 随之变化）。
	//
	// SIGPIPE 必须一并忽略：底座死后再往 stdout/stderr 写一行，Go 运行时对 fd 1/2 的 EPIPE 走
	// **默认动作**——当场终止进程，于是下面 `defer src.Close()`（关连接池与远端会话）根本来不及跑。
	// 这与 M1-6 在核心侧抓到的是同一条坑。
	IgnoreSIGPIPE()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go WatchParentExit(ctx, os.Getppid, ParentPollInterval, func() {
		glog.Warnw(context.Background(), "mcp bridge: 父进程（底座）已消失，开始停机")
		cancel()
	})

	// 数据面（M7-2）：自己读平台的库（定义 + `mcp_secrets`）。取数失败**不**让网关起不来——起不来的症状是
	// 底座那边"这个 MCP server 没起来"（连工具面都看不到），而"起来了但这一轮没有工具"是
	// 看得见的降级，且日志里有原因。
	cfg, _ := config.MustLoad()
	src, sErr := gateway.NewSource(ctx, gateway.SourceOptions{
		Cfg:        cfg,
		McpsPinned: scope.McpsPinned,
		Adapters:   assembly.AllAdapters(),
	})
	if sErr != nil {
		glog.Errorw(context.Background(), "mcp bridge: 数据面装配失败（本轮工具面为空）", "err", sErr)
	} else {
		defer func() {
			if cErr := src.Close(); cErr != nil {
				glog.Warnw(context.Background(), "mcp bridge: 数据面关闭失败", "err", cErr)
			}
		}()
	}

	var face gateway.Source
	if src != nil {
		face = src
	}
	err = gateway.Run(ctx, os.Stdin, os.Stdout, gateway.Options{
		Scope:  gateway.Scope{RuntimeID: scope.RuntimeID, McpsPinned: scope.McpsPinned},
		Source: face,
	})
	switch {
	case err == nil:
		return // stdin EOF：父进程（底座）结束，正常退出
	case errors.Is(err, context.Canceled):
		// SIGINT/SIGTERM：正常停机，不打错误行（否则每次收尾都像出了故障）。
		glog.Infow(context.Background(), "mcp bridge: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp bridge: %v\n", err)
		os.Exit(1)
	}
}
