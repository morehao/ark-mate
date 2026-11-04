package maintenance

// ——— `mate mcp automation`：定时任务的 MCP 工具面（stdio）———
//
// 底座（dsh）经 ACP 的 `mcpServers` 拉起这条命令，得到一个暴露**六个工具**的 MCP server：
// automation_create / update / list / get / delete / preview——让用户用**自然语言**创建/编辑
// 定时任务（模型把"每天 9 点在这个会话提醒我"翻成 automation_create 调用；schedule 五选一、
// 无 cron）。
//
// 三条纪律与 `mate mcp material` 一脉相承：
//  1. **stdout 只走协议**（JSON-RPC 帧）：一切诊断走 stderr（C10）。
//  2. **用例实例只在装配根造**：本文件**不 import 任何 `svc*`**（层规），只调
//     `assembly.AutomationMCPHandler` 拿一个 `mcp.Handler` + 关闭函数。
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

const automationMcpUsage = `mcp automation 用法：
  mate mcp automation
      定时任务 MCP stdio 服务端：暴露 automation_create / automation_update / automation_list /
      automation_get / automation_delete / automation_preview 六个工具（schedule 五选一，无 cron）。
      底座经 ACP 的 mcpServers 拉起本命令。
      协议流走 stdout，一切诊断走 stderr（stdout 混进一行日志就会毁掉整条会话）。
`

// automationMcp `mate mcp automation`：定时任务 MCP stdio 服务端的进程入口。
func automationMcp(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, automationMcpUsage)
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "mcp automation: 不接受参数 %v\n\n%s", args, automationMcpUsage)
		os.Exit(2)
	}

	cfg, _ := config.MustLoad()
	handler, cleanup, err := assembly.AutomationMCPHandler(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp automation: 数据面装配失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if cerr := cleanup(); cerr != nil {
			glog.Warnw(context.Background(), "mcp automation: 关闭数据面失败", "err", cerr)
		}
	}()

	IgnoreSIGPIPE()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go WatchParentExit(ctx, os.Getppid, ParentPollInterval, func() {
		glog.Warnw(context.Background(), "mcp automation: 父进程（底座）已消失，开始停机")
		cancel()
	})

	err = mcp.Serve(ctx, os.Stdin, os.Stdout, mcp.ServerInfo{
		Name:    "mate-automation",
		Version: "1",
		Instructions: "定时任务工具：用自然语言和用户确认时间（绝不把规则串给用户看），再调 automation_create/update；" +
			"schedule 五选一（once/interval/daily/weekly/monthly，无 cron）；delivery 缺省 session（投递回指定会话）。" +
			"automation_preview 只算未来时刻、不落盘，用来跟用户确认几点跑。",
	}, handler, mcp.ServerOptions{})
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled):
		glog.Infow(context.Background(), "mcp automation: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp automation: %v\n", err)
		os.Exit(1)
	}
}
