package command

import (
	"fmt"
	"os"

	"github.com/morehao/ark-forge/pkg/config"
)

// mustConfig 解析配置（含 .forge/config 目录化）。配置文件非法时打印明确错误并退出 2
// （配置错误必须在启动阶段暴露，而不是静默回落默认值）。
//
// 连 Sources 一起返回：来源不只是排障信息，还是**权限事实**——被进程 env/flag 锁定的配置键，
// 面板写入必然被覆盖，serve 据此把它转成"只读设置"（见 config.NewSettingsStore）。
func mustConfig() (config.Config, config.Sources) {
	cfg, sources, err := config.LoadResolved("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n\n配置目录: %s（FORGE_CONFIG_DIR 可改）\n", err, config.ConfigDirFromEnv())
		os.Exit(2)
	}
	return cfg, sources
}

const usageText = `forge — ark-forge 控制面 daemon（release 单二进制内嵌前端面板）

用法：
  forge serve [--addr 127.0.0.1:10001]
                                启动控制面（REST 会话动作 + WS /ws/events 实时通道 + 内嵌面板）
  forge demo [flags]            在指定 runtime 上跑一次真实 fresh 会话并打印事件（冒烟）
  forge migrate [flags]         transcript → SQLite 迁移（P1：幂等 + 事务整体回滚 + 数量校验）
  forge transcripts prune       transcript 目录显式清理（--older-than；ADR-5：不设自动删除）
  forge config [--json]         打印解析后的配置与每项来源（配置目录 .forge/config 目录化）

子命令帮助：forge serve -h / forge demo -h / forge migrate -h / forge transcripts prune -h / forge config -h
`

// Run 分发 forge 子命令，返回进程退出码（无默认子命令）。
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usageText)
		return 1
	}
	switch args[0] {
	case "serve":
		serve(args[1:])
	case "demo":
		demo(args[1:])
	case "migrate":
		migrate(args[1:])
	case "transcripts":
		if len(args) < 2 || args[1] != "prune" {
			fmt.Fprintln(os.Stderr, "transcripts 用法：forge transcripts prune --older-than <duration> [--dir <dir>]")
			os.Exit(2)
		}
		transcriptsPrune(args[2:])
	case "config":
		configCmd(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", args[0], usageText)
		os.Exit(2)
	}
	return 0
}
