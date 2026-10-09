package command

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/morehao/ark-forge/internal/version"
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
  forge capability migrate      skill 定义迁移（旧 sources[] → runtimes[]；--dry-run 可预览）
  forge capability import       ▲ 能力目录 → SQLite 导入（M6：sqlite 模式下 DB 是事实源；按行判重不覆盖）
  forge capability export       把 SQLite 里的能力定义写回能力目录（此后文件是导出物）
  forge agent import            Agent 定义目录 → SQLite 导入（M6-6：sqlite 模式下 DB 是事实源；按行判重不覆盖）
  forge agent export            把 SQLite 里的 Agent 定义写回定义目录（此后文件是导出物）
  forge automation import       定时任务目录 → SQLite 导入（M6-7：定义与运行态一并搬；按行判重不覆盖）
  forge automation export       把 SQLite 里的定时任务写回目录（定义与运行态合成同一个文件）
  forge mcp bridge --agent <id> --runtime <id>
                                平台侧 MCP stdio 网关（底座经 ACP 拉起；远程 MCP 经它代理）
  forge doctor [flags]          只读体检：资产来源与摘要、随包载荷清单与全量复算、数据目录可写性、端口占用
  forge --version [--json]      打印核心版本与 API 契约版本（--json 供机器读）

子命令帮助：forge serve -h / forge demo -h / forge migrate -h / forge transcripts prune -h / forge config -h
            forge capability {migrate,import,export} -h / forge agent {import,export} -h
            forge automation {import,export} -h / forge mcp bridge -h / forge doctor -h
`

// versionInfo `forge --version --json` 的输出形状（M4-1）。
//
// 键序即结构体字段序（确定性纪律 C9）：打包期 `build-release-json.mjs` 读它填 release.json 的
// `coreVersion` / `apiContract`——**唯一取法**就是这里，不允许脚本另抄一份版本号或猜契约代数。
type versionInfo struct {
	Version     string `json:"version"`
	APIContract string `json:"apiContract"`
}

// versionOutput 版本信息的一行输出（纯函数：单测直接断言字节，不重定向 stdout）。
//
// 非 JSON 形态只有版本号一行（人用、脚本 `$(forge --version)` 取值都最省事）；
// JSON 形态带上 `apiContract`——它是"契约本身是哪一代"，与核心版本号解耦（见 internal/version）。
func versionOutput(asJSON bool) string {
	if !asJSON {
		return version.Version + "\n"
	}
	// json.Marshal 对结构体按字段序输出，且会正确转义；不手拼字符串。
	b, err := json.Marshal(versionInfo{Version: version.Version, APIContract: version.APIContract})
	if err != nil {
		// 两个 string 字段的 Marshal 不可能失败；真失败了也不能回一份半截 JSON。
		fmt.Fprintf(os.Stderr, "版本信息序列化失败: %v\n", err)
		os.Exit(2)
	}
	return string(b) + "\n"
}

// Run 分发 forge 子命令，返回进程退出码（无默认子命令）。
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usageText)
		return 1
	}
	switch args[0] {
	case "--version", "-version", "-v":
		// 打包期消费点（M4-1）：`forge --version` 是 release.json 的 coreVersion 唯一取法。
		// 多余参数一律报错而不是忽略——"forge --version --json --x" 静默成功会让脚本以为
		// 自己拿到了 JSON。
		asJSON := false
		switch {
		case len(args) == 1:
		case len(args) == 2 && args[1] == "--json":
			asJSON = true
		default:
			fmt.Fprintln(os.Stderr, "用法：forge --version [--json]")
			os.Exit(2)
		}
		fmt.Print(versionOutput(asJSON))
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
	case "capability":
		capabilityCmd(args[1:])
	case "agent":
		agentCmd(args[1:])
	case "automation":
		automationCmd(args[1:])
	case "mcp":
		mcpCmd(args[1:])
	case "doctor":
		doctor(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", args[0], usageText)
		os.Exit(2)
	}
	return 0
}
