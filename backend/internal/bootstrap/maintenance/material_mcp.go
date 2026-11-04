package maintenance

// ——— `mate mcp material`：资料库的文件级 MCP 工具面（stdio）———
//
// 底座（dsh）经 ACP 的 `mcpServers` 拉起这条命令，得到一个只暴露**文件级**工具（列目录 / 节点信息 /
// 上传）与**产物显式收录**（library_import_artifact）的 MCP server；**没有 library_search**
// （本期只做资料库、不做知识库，无语义检索）。
//
// 三条纪律与 `mate mcp bridge` 一脉相承：
//  1. **stdout 只走协议**（JSON-RPC 帧）：一切诊断走 stderr（C10，日志出口已由 `mcp` 子命令统一配到文件）。
//  2. **用例实例只在装配根造**：本文件**不 import 任何 `svc*`**（层规），只调
//     `assembly.MaterialMCPHandler` 拿一个 `mcp.Handler` + 关闭函数。
//  3. **父进程自保**：stdin EOF 即退出（首要），另有 ppid 轮询兜底 + 忽略 SIGPIPE。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
	"github.com/morehao/golib/glog"
)

const materialMcpUsage = `mcp material 用法：
  mate mcp material
      资料库 MCP stdio 服务端：暴露 library_list_nodes / library_node_info / library_upload /
      library_import_artifact 四个工具（无 search——本期只做资料库、不做知识库）。
      底座经 ACP 的 mcpServers 拉起本命令。
      协议流走 stdout，一切诊断走 stderr（stdout 混进一行日志就会毁掉整条会话）。
`

// materialMcp `mate mcp material`：资料库 MCP stdio 服务端的进程入口。
func materialMcp(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, materialMcpUsage)
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "mcp material: 不接受参数 %v\n\n%s", args, materialMcpUsage)
		os.Exit(2)
	}

	cfg, _ := config.MustLoad()
	handler, cleanup, err := assembly.MaterialMCPHandler(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp material: 数据面装配失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if cerr := cleanup(); cerr != nil {
			glog.Warnw(context.Background(), "mcp material: 关闭数据面失败", "err", cerr)
		}
	}()

	IgnoreSIGPIPE()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go WatchParentExit(ctx, os.Getppid, ParentPollInterval, func() {
		glog.Warnw(context.Background(), "mcp material: 父进程（底座）已消失，开始停机")
		cancel()
	})

	err = mcp.Serve(ctx, os.Stdin, os.Stdout, mcp.ServerInfo{
		Name:         "mate-material",
		Version:      "1",
		Instructions: "资料库工具：library_list_nodes 浏览目录、library_node_info 读节点（drive 给本地只读路径，交给 read）、library_upload 上传本地文件成节点、library_import_artifact 把会话产物收录进资料库。本期不提供语义检索。",
	}, handler, mcp.ServerOptions{})
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled):
		glog.Infow(context.Background(), "mcp material: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp material: %v\n", err)
		os.Exit(1)
	}
}
