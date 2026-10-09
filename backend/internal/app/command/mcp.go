package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/morehao/ark-forge/internal/gateway"
	"github.com/morehao/ark-forge/pkg/mcp"
)

// mcpCmd 分发 `forge mcp <动作>`（M7-1：网关子命令）。
func mcpCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, mcpUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "bridge":
		mcpBridge(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 mcp 动作 %q\n\n%s", args[0], mcpUsage)
		os.Exit(2)
	}
}

const mcpUsage = `mcp 用法：
  forge mcp bridge --agent <agentID> --runtime <runtimeID>
      平台侧 MCP stdio 网关：以 stdio 上的 MCP 服务端形态暴露该 scope 下的远程 MCP 工具
      （底座经 ACP 的 mcpServers 把这条命令拉起来；参数只带 scope，不带 server 列表）
      协议流走 stdout，**一切诊断走 stderr**（stdout 混进一行日志就会毁掉整条会话）
`

// mcpBridge `forge mcp bridge`：平台侧 MCP stdio 网关的进程入口。
//
// 三条纪律（都不是风格问题）：
//  1. **stdout 只走协议**（C10）：日志 handler 显式绑 stderr，不用 fmt.Print 写任何诊断；
//     连用法/错误都走 stderr —— 底座把 stdout 当 JSON-RPC 流读。
//  2. **参数解析与注入端同源**（P-2）：用 `mcp.ParseScopeArgs` 解 `mcp.GatewayBridgeArgs`
//     拼出来的那串。未知参数**当场失败**——静默忽略会表现成"网关起来了但 scope 不对"，
//     而 scope 不对的症状是"工具面不对"，最难查的一类。
//  3. **不读配置文件**：网关是底座拉起的子进程，它的取数范围只由 scope 决定（M7-2 自己开
//     store）；这里不去解析 FORGE_* —— 那些是**核心进程**的配置，混进来就有了第二条事实源。
func mcpBridge(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, mcpUsage)
		return
	}
	agentID, runtimeID, err := mcp.ParseScopeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp bridge: %v\n\n%s", err, mcpUsage)
		os.Exit(2)
	}

	// 日志一律 stderr（显式构造，不依赖 slog.Default 的偶然取值——那正是 C10 的隐患）。
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err = gateway.Run(ctx, os.Stdin, os.Stdout, gateway.Options{
		Scope:  gateway.Scope{AgentID: agentID, RuntimeID: runtimeID},
		Logger: logger,
		// Source 在 M7-2 接线：网关在被注入之前（M7-3）对真实会话不可达，所以骨架形态
		// （空工具面）不会出现在用户面前；接线失败会在 tools/list 上响亮地 WARN。
	})
	switch {
	case err == nil:
		return // stdin EOF：父进程（底座）结束，正常退出
	case errors.Is(err, context.Canceled):
		// SIGINT/SIGTERM：正常停机，不打错误行（否则每次收尾都像出了故障）。
		logger.Info("mcp bridge: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp bridge: %v\n", err)
		os.Exit(1)
	}
}
