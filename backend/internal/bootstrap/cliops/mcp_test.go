package cliops

// `mate mcp bridge` 的**进程级**用例：帧形状与 stdout 纯度已经在包内验过
// （`internal/infra/protocol/mcp/server_test.go`、`internal/drivers/gateway/gateway_test.go`），这里钉的是**进程入口**：
// 子命令分发、参数解析、退出码，以及"诊断绝不写 stdout"这条在真实二进制上是否成立。
//
// 为什么真编一个二进制：网关是**底座拉起的子进程**，它的入口形态（argv → 退出码 → 两个流）
// 就是它的对外契约；在进程内调函数会把 os.Stdin/os.Stdout 变成全局可变状态，既测不准也污染
// 其他用例。构建走一次性 `go build`（有缓存，秒级）。

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildMateCLI 把 `./cmd/mate` 编到临时目录（同一测试内多次调用只编一次）。
func buildMateCLI(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "mate")
	build := exec.Command("go", "build", "-o", bin, "./cmd/mate")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 CLI 失败: %v\n%s", err, out)
	}
	return bin
}

// readCLILogs 读某个私有数据目录下 glog 文件 writer 写出的全部日志。
func readCLILogs(t *testing.T, home string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(filepath.Join(home, "logs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // 没有 logs 目录 = 一条日志都没写，交由断言报错
		}
		b, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		sb.Write(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读网关日志目录: %v", err)
	}
	return sb.String()
}

// isolatedCLIEnv 子进程的私有环境：网关按与核心进程同一个解析器定位数据面，所以在用户默认
// 环境里跑它就会去读（并 **Migrate**！）用户自己的 `~/.ark-mate/mate.db`。测试必须给私有
// MATE_HOME/MATE_DB——否则用例的结果取决于"当前机器上恰好有没有库"，还会顺手改写它。
func isolatedCLIEnv(t *testing.T) []string {
	t.Helper()
	env, _ := isolatedCLIEnvHome(t)
	return env
}

// isolatedCLIEnvHome 同上，另外返回私有数据目录——需要读网关日志文件的用例用它
// （网关的诊断出口是文件，见下）。
func isolatedCLIEnvHome(t *testing.T) ([]string, string) {
	t.Helper()
	home := t.TempDir()
	return append(os.Environ(),
		"MATE_HOME="+home, "MATE_DB="+filepath.Join(home, "mate.db"),
		"MATE_CONFIG_DIR="+filepath.Join(home, "config")), home
}

func TestMcpBridgeCLI(t *testing.T) {
	bin := buildMateCLI(t)

	isolated, home := isolatedCLIEnvHome(t)

	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cli","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ghost__tool","arguments":{}}}`,
	}
	cmd := exec.Command(bin, "mcp", "bridge", "--runtime", "r1")
	cmd.Env = isolated
	cmd.Stdin = strings.NewReader(strings.Join(frames, "\n") + "\n")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("网关应正常退出（stdin EOF → 0）: %v\nstderr:\n%s", err, stderr.String())
	}

	// stdout 逐行必须是 JSON-RPC 帧（C10：一行日志就毁掉整条会话）。
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 条响应（通知不响应），got %d：\n%s", len(lines), stdout.String())
	}
	ids := map[float64]bool{}
	for i, line := range lines {
		var msg struct {
			JSONRPC string           `json:"jsonrpc"`
			ID      *json.RawMessage `json:"id"`
			Result  json.RawMessage  `json:"result"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout 第 %d 行不是 JSON-RPC（stdout 被污染）：%q", i+1, line)
		}
		if msg.JSONRPC != "2.0" {
			t.Fatalf("stdout 第 %d 行 jsonrpc 字段不是 2.0：%q", i+1, line)
		}
		if msg.ID != nil {
			var id float64
			if err := json.Unmarshal(*msg.ID, &id); err == nil {
				ids[id] = true
			}
		}
	}
	for _, want := range []float64{1, 2, 3} {
		if !ids[want] {
			t.Fatalf("缺少 id=%v 的响应：%s", want, stdout.String())
		}
	}
	// 诊断写进日志出口（**不是 stdout**）：网关启动、scope、数据面取数失败的原因都在这里
	// （空面必须响亮）。
	//
	// 出口自 2026-10-10 起是 `<MATE_HOME>/logs/<日期>/ark-mate-mcp_full.log`：glog 的
	// console writer 写的是 os.Stdout（golib 里硬编码），而这里的 stdout 是协议流（C10），
	// 所以 mcp 子命令只能用文件 writer（见 internal/bootstrap/maintenance/logging.go）。
	logs := readCLILogs(t, home)
	// 空工具面**不再**走"取数失败"那条（v19 起 scope 只有 runtime，数据面就绪即空面）：
	// 那条失败出口由 `internal/drivers/gateway/gateway_test.go` 的单测负责。
	for _, want := range []string{"mcp bridge", `"runtime":"r1"`, `"pinned":false`, "数据面就绪"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("网关日志应含 %q：\n%s", want, logs)
		}
	}
	if stderr.String() != "" {
		t.Fatalf("诊断不该再走 stderr（那是 slog 时代的出口）：\n%s", stderr.String())
	}

	// 参数漂移必须当场失败，且**不得往 stdout 写任何东西**（那里是协议流）。
	// `--agent` 正是 v19 撤销的那个参数：它必须与 `--servers` 一样落进"未知参数"（而不是被静默忽略）。
	bad := exec.Command(bin, "mcp", "bridge", "--agent", "a1", "--runtime", "r1", "--servers", "a,b")
	bad.Env = isolated
	bad.Stdin = strings.NewReader("")
	var badOut, badErr strings.Builder
	bad.Stdout = &badOut
	bad.Stderr = &badErr
	err := bad.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("未知参数应以退出码 2 失败，got %v", err)
	}
	if badOut.String() != "" {
		t.Fatalf("参数错误时 stdout 必须为空（协议流）：%q", badOut.String())
	}
	if !strings.Contains(badErr.String(), "未知参数") {
		t.Fatalf("参数错误要说清楚：%q", badErr.String())
	}

	// 用法文本要能被 `mate mcp bridge -h` 找到（承接口径，不进 README 路由表——它不是 HTTP 端点）。
	help := exec.Command(bin, "mcp", "bridge", "-h")
	help.Env = isolated
	var helpOut, helpErr strings.Builder
	help.Stdout = &helpOut
	help.Stderr = &helpErr
	if err := help.Run(); err != nil {
		t.Fatalf("-h 应正常退出: %v", err)
	}
	if helpOut.String() != "" || !strings.Contains(helpErr.String(), "--runtime") {
		t.Fatalf("-h 的说明也走 stderr（stdout 是协议流）：stdout=%q stderr=%q", helpOut.String(), helpErr.String())
	}

	// 顶层 usage 要有 mcp 行（子命令可发现性由 CLI 文本承担，没有 HTTP 路由可查）。
	top := exec.Command(bin, "help")
	top.Env = isolated
	var topOut strings.Builder
	top.Stdout = &topOut
	if err := top.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(topOut.String(), "mate mcp bridge") {
		t.Fatalf("顶层 usage 应列出 mcp bridge：\n%s", topOut.String())
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
}
