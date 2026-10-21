package maintenance

// 测试二进制里的日志 driver 注册（见 internal/bootstrap/app/logging_test.go 的说明）。
//
// 本包的测试直接调 `Run`/各子命令函数（`initProcessLogger` 会被调到），但
// `go test ./internal/bootstrap/app/command` 的二进制不含 `cmd/mate` 的匿名引入，
// 因此 store.Open 会因为 "unknown LoggerType zap" 失败。
import (
	"testing"

	"github.com/morehao/golib/glog"
	_ "github.com/morehao/golib/glog/driver/zap"
)

// TestDataStdoutCommandsNeverUseConsoleWriter "stdout 是数据面"的子命令**绝不能挂 console writer**。
//
// glog 的 console writer 写的是 **os.Stdout**（golib 里硬编码，没有 stderr 选项），而：
//   - `mate mcp` 的 stdout 是 JSON-RPC 协议流（C10）：一行日志就让底座把日志当协议帧解析，
//     症状是"工具莫名不可用"；
//   - `--json` 系（config/doctor…）的 stdout 是机器可读产物：混一行日志就毁掉 CI/诊断包。
//
// 这是**配置层**的判据；协议层那半（stdout 真的只有帧、诊断真的进了文件）由 pkg/mcp 的
// stdout 纯度用例与 TestMcpBridgeCLI 真起进程验证。
func TestDataStdoutCommandsNeverUseConsoleWriter(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantLog bool // true = 应装出口（文件），false = 应保持静默
	}{
		{"mcp 协议流", []string{"mcp", "bridge", "--agent", "a", "--runtime", "r"}, true},
		{"doctor --json", []string{"doctor", "--json"}, false},
		{"config --json", []string{"config", "--json"}, false},
		{"版本快路径", []string{"--version", "--json"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.args[0]
			if isVersionFastPath(cmd) {
				if tc.wantLog {
					t.Fatal("版本快路径不该装出口")
				}
				return
			}
			cfg, ok := dataStdoutLogConfig(cmd, glog.InfoLevel)
			if ok != tc.wantLog {
				t.Fatalf("装出口 = %v，期望 %v", ok, tc.wantLog)
			}
			if !ok {
				return
			}
			if len(cfg.Writers) == 0 {
				t.Fatal("日志配置没有任何 writer：诊断会被静默丢弃")
			}
			for _, w := range cfg.Writers {
				if w.Type == glog.WriterConsole {
					t.Fatalf("stdout 是数据面的子命令不得含 console writer（它写 os.Stdout）: %+v", w)
				}
			}
		})
	}
}

// TestNormalCommandsUseConsole 反向那一半：人类可读输出的子命令必须能看到日志。
//
// 没有这条，把出口改成"一律静默"也会让上面那条保持绿。
func TestNormalCommandsUseConsole(t *testing.T) {
	cfg := consoleLogConfig(logServiceName, glog.InfoLevel)
	if len(cfg.Writers) != 1 || cfg.Writers[0].Type != glog.WriterConsole {
		t.Fatalf("默认子命令（serve/migrate/agent…）应把日志打到 console：%+v", cfg.Writers)
	}
}
