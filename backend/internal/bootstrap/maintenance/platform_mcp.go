package maintenance

// ——— `mate mcp platform`：平台能力 MCP 工具面（stdio）———
//
// 聚合两域：automation_*（定时任务）+ expert_*（专家定义）。会话默认注入的就是这一条
// （见 internal/sessionruntime/platform_mcp.go），模型据此用自然语言创建/编辑专家与定时任务。
//
// 三条纪律与 `mate mcp automation` / `mate mcp material` 一脉相承：
//  1. **stdout 只走协议**（JSON-RPC 帧）：一切诊断走 stderr（C10）。
//  2. **用例实例只在装配根造**：本文件**不 import 任何 `svc*`**（层规），只调
//     `assembly.PlatformMCPHandler` 拿一个 `mcp.Handler` + 关闭函数。
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

const platformMcpUsage = `mcp platform 用法：
  mate mcp platform
      平台能力 MCP stdio 服务端，聚合两域：
        automation_*  定时任务（create/update/list/get/delete/preview；schedule 五选一，无 cron）
        expert_*      专家定义（create/update/list/get/delete/set_enabled；角色设定 ≤4096 字节）
      底座经 ACP 的 mcpServers 拉起本命令（会话默认注入的就是这一条）。
      协议流走 stdout，一切诊断走 stderr（stdout 混进一行日志就会毁掉整条会话）。
`

// platformMcp `mate mcp platform`：平台能力 MCP stdio 服务端的进程入口。
func platformMcp(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, platformMcpUsage)
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "mcp platform: 不接受参数 %v\n\n%s", args, platformMcpUsage)
		os.Exit(2)
	}

	cfg, _ := config.MustLoad()
	handler, cleanup, err := assembly.PlatformMCPHandler(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp platform: 数据面装配失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if cerr := cleanup(); cerr != nil {
			glog.Warnw(context.Background(), "mcp platform: 关闭数据面失败", "err", cerr)
		}
	}()

	IgnoreSIGPIPE()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go WatchParentExit(ctx, os.Getppid, ParentPollInterval, func() {
		glog.Warnw(context.Background(), "mcp platform: 父进程（底座）已消失，开始停机")
		cancel()
	})

	err = mcp.Serve(ctx, os.Stdin, os.Stdout, mcp.ServerInfo{
		Name:    "mate-platform",
		Version: "1",
		Instructions: "平台能力工具：automation_*（定时任务，schedule 五选一、无 cron、delivery 缺省 session）与 " +
			"expert_*（专家定义，角色设定 ≤4096 字节）。**一律用自然语言与用户确认后再建**，" +
			"绝不把规则串/schema 细节甩给用户。专家是「新建会话时的预设」——建好不会让当前会话变成该专家。",
	}, handler, mcp.ServerOptions{})
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled):
		glog.Infow(context.Background(), "mcp platform: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp platform: %v\n", err)
		os.Exit(1)
	}
}
