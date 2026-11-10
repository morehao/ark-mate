package apphost_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/infra/apphost/hostkit"
)

// ——— 真·子进程隔离测试（re-exec 测试二进制当 helper host）———
//
// 与 host_test.go 的进程内 fake 不同，这里**真的**拉起一个独立 OS 进程（把当前测试二进制以
// APPHOST_HELPER=1 重新执行，TestMain 见到该环境变量即跑 hostkit.Serve 后退出），验证：
//   - 真进程的握手/RPC 往返（stdin/stdout 跨进程）；
//   - 子进程崩溃（请求 "panic" 工具 → handler 不 recover、进程直接 panic 退出）→ 父捕获、Host FAILED，
//     且父进程本身纹丝不动（隔离）。
//
// 这条覆盖了「绝不与内核同进程」这条安全基线的真实形态（决策 v37）。

const helperEnv = "APPHOST_HELPER"

// TestMain 在 helper 模式下充当子进程 host 入口；正常模式下跑全部测试。
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		runHelperHost()
		return
	}
	os.Exit(m.Run())
}

// runHelperHost 是本测试二进制作为子进程 host 时的 body：跑 hostkit.Serve，handler 里对 "panic"
// 工具**不 recover 直接 panic**（模拟陌生代码崩溃，验证进程级隔离；hostkit.Serve 会 recover，
// 但这里为了制造真崩溃，用一个会真正 os.Exit 的 handler——见下）。
func runHelperHost() {
	srv := hostkit.NewServer(&crashingHandlers{})
	_ = srv.Serve(os.Stdin, os.Stdout)
}

// crashingHandlers：正常工具返回 echo；工具名 "crash" 时直接 os.Exit(3) 模拟进程级崩溃（比 panic
// 更真实——真实陌生代码可能 abort/segfault，进程直接没了，而不是被 hostkit recover）。
type crashingHandlers struct{}

func (crashingHandlers) Describe() hostkit.DescribeInfo {
	return hostkit.DescribeInfo{PackageID: "crashy", PackageVersion: "1", APIVersion: "^1",
		Impls: []string{"crashy.sync"}, Tools: []string{"crashy_ping", "crashy_crash"}}
}
func (crashingHandlers) ListImpls() []string { return []string{"crashy.sync"} }
func (crashingHandlers) ListTools() []apphost.ToolSpec {
	return []apphost.ToolSpec{{Name: "crashy_ping"}, {Name: "crashy_crash"}}
}
func (crashingHandlers) InvokeTool(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	if name == "crashy_crash" {
		os.Exit(3) // 进程级崩溃：模拟陌生代码 abort/segfault
	}
	out, _ := json.Marshal(map[string]any{"echo": json.RawMessage(args)})
	return out, false, nil
}
func (crashingHandlers) ApplySlot(string, map[string]string) error { return nil }
func (crashingHandlers) DisposeSlot(string) error                  { return nil }

// execSelfSpawn 返回一个 SpawnFunc：把当前测试二进制以 helper 模式拉起（真子进程）。
func execSelfSpawn(t *testing.T) apphost.SpawnFunc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试可执行文件失败：%v", err)
	}
	return func(ctx context.Context, _ string) (io.WriteCloser, io.ReadCloser, func() error, func() error, error) {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^$") // 不跑测试，仅进 TestMain
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, nil, err
		}
		return stdin, stdout, func() error { return cmd.Wait() }, func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		}, nil
	}
}

// 真子进程：握手 + listTools + invokeTool 往返。
func TestRealSubprocessRPC(t *testing.T) {
	h := apphost.NewHost(apphost.HostConfig{
		PackageID: "crashy", PkgDir: "/nonexistent", APIVersion: "1.0",
		Spawn: execSelfSpawn(t), CallTimeout: 5 * time.Second, HandshakeTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("拉起真子进程并握手失败：%v", err)
	}
	defer h.Close()

	if h.State() != apphost.StateReady {
		t.Fatalf("真子进程握手后应 Ready，得到 %s", h.State())
	}
	tools, err := h.ListTools()
	if err != nil || len(tools) != 2 {
		t.Fatalf("listTools 失败：tools=%v err=%v", tools, err)
	}
	content, isErr, err := h.InvokeTool("crashy_ping", json.RawMessage(`{"x":1}`))
	if err != nil || isErr {
		t.Fatalf("invokeTool 失败：isErr=%v err=%v", isErr, err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(content, &got); err != nil || string(got["echo"]) == "" {
		t.Fatalf("invokeTool 未回显：%s err=%v", content, err)
	}
}

// 真子进程：请求 crash 工具 → 子进程 os.Exit(3) → 父捕获、Host FAILED，父进程不受影响。
func TestRealSubprocessCrashIsolated(t *testing.T) {
	h := apphost.NewHost(apphost.HostConfig{
		PackageID: "crashy", PkgDir: "/nonexistent", APIVersion: "1.0",
		Spawn: execSelfSpawn(t), CallTimeout: 5 * time.Second, HandshakeTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("拉起真子进程失败：%v", err)
	}
	defer h.Close()

	// 触发进程级崩溃：这次调用会因 host 崩溃而失败（host_gone / timeout，取决于时序）。
	_, _, _ = h.InvokeTool("crashy_crash", nil)

	deadline := time.Now().Add(3 * time.Second)
	for h.State() != apphost.StateFailed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.State() != apphost.StateFailed {
		t.Fatalf("子进程崩溃后 Host 应变 Failed，得到 %s", h.State())
	}
	// 父进程（本测试进程）仍在跑——崩溃被隔离在子进程。
	t.Logf("父进程存活；Host 状态=%s 原因=%q", h.State(), h.Reason())
}
