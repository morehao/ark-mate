package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/golib/glog"
)

// logServiceName 日志里的服务名（glog 的 service 字段）。
const logServiceName = "ark-mate"

// initProcessLogger 装配进程日志出口。
//
// 分工与 ark-go-starter 一致：zap driver 的**匿名引入在装配根**（cmd/mate/main.go），
// 配置与 InitLogger 在这里；pkg/ 下的包不替调用方决定日志出口（internal/adapters/outbound/sqlite/db 只声明
// 自己要什么级别，见其 ormLogConfig）。
//
// **唯一的硬约束**：glog 的 console writer 写的是 **os.Stdout**（golib 里硬编码，没有 stderr
// 选项），所以凡是把 stdout 当**数据面**的子命令都不能用它——
//   - `mate mcp`：stdout 是 JSON-RPC 协议流（mcp.go 顶部纪律 C10），一行日志就让底座把
//     日志当协议帧解析，症状是"工具莫名不可用"；诊断改走 `<MATE_HOME>/logs/` 下的文件；
//   - `--json` 系（`mate config --json` / `mate doctor --json` …）：stdout 是机器可读产物，
//     混进一行日志就毁掉了 CI/诊断包那条消费链；
//   - `mate --version`：打包期的快路径（release.json 的 coreVersion 唯一取法），它只往
//     stdout 打一行、本进程没有任何诊断可记，因此**既不装出口也不碰文件系统**。
//
// 其余子命令走 console（stdout）+ info：glog 的内置默认是 **debug + console**，不显式提级
// 会把 gorm 的每条 SQL、每次轮询都打出来。
//
// 初始化失败**不阻断**命令：日志出口坏掉不该让 `mate migrate` 这类活干不了；但必须说清楚，
// 否则症状是"这个进程完全没有日志"。
func InitProcessLogger(args []string) {
	if len(args) == 0 {
		return
	}
	cmd := args[0]
	switch {
	case isVersionFastPath(cmd):
		return
	case cmd == "mcp" || hasJSONFlag(args):
		cfg, ok := dataStdoutLogConfig(cmd, glog.InfoLevel)
		if ok {
			installLogger(cfg)
		}
	default:
		installLogger(consoleLogConfig(logServiceName, glog.InfoLevel))
	}
}

// RaiseLogVerbosity 把本进程日志降到 debug（`mate demo` 冒烟要看 runtime stderr 与每一步诊断）。
//
// glog 是包级单例，重装一份配置即可——这也是它取代 slog 后"提级"的唯一姿势
// （slog 时代是 slog.SetLogLoggerLevel）。
func RaiseLogVerbosity() {
	installLogger(consoleLogConfig(logServiceName, glog.DebugLevel))
}

func installLogger(cfg *glog.LogConfig) {
	if err := glog.InitLogger(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s: 初始化日志失败（本次运行无日志输出）: %v\n", logServiceName, err)
	}
}

// consoleLogConfig 人类可读输出的默认出口（stdout + 指定级别）。
func consoleLogConfig(service string, level glog.Level) *glog.LogConfig {
	return &glog.LogConfig{
		Service:    service,
		Level:      level,
		LoggerType: glog.LoggerTypeZap,
		Writers:    []glog.WriterConfig{{Type: glog.WriterConsole, Level: level}},
	}
}

// dataStdoutLogConfig 给出"stdout 是数据面"的子命令的日志出口。
//
// ok=false 表示**不装出口**（保持 glog 的默认态：driver 的匿名引入发生在 glog 包初始化
// 之后——driver 依赖 glog，Go 保证被依赖者先初始化——所以那一刻注册表还是空的、默认 logger
// 落在 nop 上。刻意不 InitLogger 就保住了这个 nop）。
//
// 除了 mcp（长命进程，诊断要能事后查，走文件），其余都是一次性命令且诊断已经在自己的输出里，
// 直接静默——顺带避免 `mate --version` 这类打包快路径去建日志目录。
func dataStdoutLogConfig(cmd string, level glog.Level) (*glog.LogConfig, bool) {
	if cmd != "mcp" {
		return nil, false
	}
	return &glog.LogConfig{
		Service:    logServiceName + "-mcp",
		Level:      level,
		LoggerType: glog.LoggerTypeZap,
		Writers: []glog.WriterConfig{{
			Type:  glog.WriterFile,
			Level: level,
			Dir:   filepath.Join(config.HomeDirFromEnv(), "logs"),
		}},
	}, true
}

// isVersionFastPath 报告是否是 `mate --version` 的几种写法（Run 把它们当**子命令**处理）。
func isVersionFastPath(cmd string) bool {
	switch cmd {
	case "--version", "-version", "-v":
		return true
	default:
		return false
	}
}

// hasJSONFlag 报告参数里有没有 `--json`（机器可读输出的开关）。
func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || strings.HasPrefix(a, "--json=") {
			return true
		}
	}
	return false
}
