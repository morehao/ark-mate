package apphost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/infra/apphost/hostkit"
)

// ——— 测试用样例 Handlers（进程内，经 hostkit.Serve 应答）———
//
// 用它把「协议循环 + RPC 客户端 + Host 生命周期」串起来测，无需真正编译子进程二进制。
// 真·进程隔离另有 re-exec 测试（proc_test.go）。

const (
	sampleImplID = "sample.sync"
	sampleTool   = "fooai_query"
)

// sampleHandlers 是一个最小可用的 Handlers：一个 impl、一个工具、可配置"调工具即 panic / 慢 / 大"。
type sampleHandlers struct {
	mode string // "" normal | "panic" | "slow" | "big"
	mu   sync.Mutex
}

func (h *sampleHandlers) Describe() hostkit.DescribeInfo {
	return hostkit.DescribeInfo{
		PackageID: "sample-app", PackageVersion: "1.0.0", APIVersion: "^1",
		Impls: []string{sampleImplID}, Tools: []string{sampleTool},
	}
}
func (h *sampleHandlers) ListImpls() []string { return []string{sampleImplID} }
func (h *sampleHandlers) ListTools() []apphost.ToolSpec {
	return []apphost.ToolSpec{{Name: sampleTool, Description: "样例工具"}}
}
func (h *sampleHandlers) InvokeTool(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	switch h.mode {
	case "panic":
		panic("synthetic tool panic")
	case "slow":
		time.Sleep(300 * time.Millisecond)
	case "big":
		pad := make([]byte, 64*1024)
		for i := range pad {
			pad[i] = 'x'
		}
		b, _ := json.Marshal(map[string]string{"blob": string(pad)})
		return b, false, nil
	}
	if name != sampleTool {
		return nil, false, fmt.Errorf("未知工具 %q", name)
	}
	out, _ := json.Marshal(map[string]any{"echo": json.RawMessage(args), "ok": true})
	return out, false, nil
}
func (h *sampleHandlers) ApplySlot(implID string, _ map[string]string) error {
	if implID != sampleImplID {
		return fmt.Errorf("未知 impl %q", implID)
	}
	return nil
}
func (h *sampleHandlers) DisposeSlot(string) error { return nil }

// fakeProc 用一个进程内 hostkit.Server 冒充子进程：Client/Host 看到真正的「stdin 写 / stdout 读」。
type fakeProc struct {
	handlers hostkit.Handlers
	toHostW  *io.PipeWriter
	exitCh   chan error
	killOnce sync.Once
}

func fakeSpawn(h hostkit.Handlers) (apphost.SpawnFunc, *fakeProc) {
	fp := &fakeProc{handlers: h}
	return func(ctx context.Context, _ string) (io.WriteCloser, io.ReadCloser, func() error, func() error, error) {
		return fp.start()
	}, fp
}

func (fp *fakeProc) start() (io.WriteCloser, io.ReadCloser, func() error, func() error, error) {
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	fp.toHostW = toHostW
	fp.exitCh = make(chan error, 1)
	srv := hostkit.NewServer(fp.handlers)
	go func() {
		err := srv.Serve(toHostR, fromHostW)
		_ = fromHostW.Close()
		fp.exitCh <- err
	}()
	wait := func() error { return <-fp.exitCh }
	kill := func() error {
		fp.killOnce.Do(func() { _ = toHostW.CloseWithError(errors.New("killed")) })
		return nil
	}
	return toHostW, fromHostR, wait, kill, nil
}

func newTestHost(h hostkit.Handlers, mut func(*apphost.HostConfig)) (*apphost.Host, *fakeProc) {
	spawn, fp := fakeSpawn(h)
	cfg := apphost.HostConfig{
		PackageID: "sample-app", PkgDir: "/nonexistent", APIVersion: "1.0",
		Spawn: spawn, CallTimeout: 2 * time.Second, HandshakeTimeout: 2 * time.Second,
	}
	if mut != nil {
		mut(&cfg)
	}
	return apphost.NewHost(cfg), fp
}

// 握手 → listImpls → listTools → invokeTool 往返 → apply/dispose → health。
func TestHostHandshakeAndRPC(t *testing.T) {
	h, _ := newTestHost(&sampleHandlers{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	defer h.Close()
	if h.State() != apphost.StateReady {
		t.Fatalf("握手后应 StateReady，得到 %s", h.State())
	}
	desc := h.HandshakeResult()
	if desc.PackageID != "sample-app" || len(desc.Impls) != 1 || desc.Impls[0] != sampleImplID {
		t.Fatalf("握手自述不符合预期：%+v", desc)
	}

	impls, err := h.ListImpls()
	if err != nil || len(impls) != 1 || impls[0] != sampleImplID {
		t.Fatalf("listImpls 失败：impls=%v err=%v", impls, err)
	}
	tools, err := h.ListTools()
	if err != nil || len(tools) != 1 || tools[0].Name != sampleTool {
		t.Fatalf("listTools 失败：tools=%v err=%v", tools, err)
	}

	content, isErr, err := h.InvokeTool(sampleTool, json.RawMessage(`{"q":"hi"}`))
	if err != nil || isErr {
		t.Fatalf("invokeTool 失败：isErr=%v err=%v", isErr, err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("invokeTool 结果非 JSON：%v (%s)", err, content)
	}
	if string(got["echo"]) == "" {
		t.Fatalf("invokeTool 未回显参数：%s", content)
	}

	if err := h.ApplySlot(sampleImplID, nil); err != nil {
		t.Fatalf("applySlot 失败：%v", err)
	}
	if err := h.DisposeSlot(sampleImplID); err != nil {
		t.Fatalf("disposeSlot 失败：%v", err)
	}
	if hr, err := h.Health(); err != nil || !hr.OK {
		t.Fatalf("health 失败：%+v err=%v", hr, err)
	}
}

// 未知工具 → tool_failed 错误（映射到 failure，是业务/协议错误而非传输错误）。
func TestHostInvokeUnknownTool(t *testing.T) {
	h, _ := newTestHost(&sampleHandlers{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	defer h.Close()
	_, _, err := h.InvokeTool("no_such_tool", nil)
	if err == nil {
		t.Fatal("未知工具应返回错误")
	}
	var fe *failure.Error
	if !errors.As(err, &fe) || fe.Class == "" {
		t.Fatalf("错误应是带分类的 *failure.Error，得到 %v", err)
	}
}

// handler 内部 panic → hostkit recover 成 internal 错误帧；Host 仍 Ready（单请求失败隔离，进程不崩）。
func TestHostHandlerPanicIsIsolated(t *testing.T) {
	h, _ := newTestHost(&sampleHandlers{mode: "panic"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	defer h.Close()

	if _, _, err := h.InvokeTool(sampleTool, nil); err == nil {
		t.Fatal("handler panic 应表现为调用失败")
	}
	if h.State() != apphost.StateReady {
		t.Fatalf("单请求 panic 后 Host 应仍 Ready，得到 %s", h.State())
	}
	if hr, herr := h.Health(); herr != nil || !hr.OK {
		t.Fatalf("panic 后 health 应仍通过：%+v err=%v", hr, herr)
	}
}

// 单次 RPC 超时：handler 故意慢 + 很短 CallTimeout → 回 ClassConnection 错误，快速返回。
func TestHostRPCTimeout(t *testing.T) {
	h, _ := newTestHost(&sampleHandlers{mode: "slow"}, func(c *apphost.HostConfig) {
		c.CallTimeout = 50 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	defer h.Close()

	start := time.Now()
	_, _, err := h.InvokeTool(sampleTool, nil)
	if err == nil {
		t.Fatal("慢调用应超时")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("超时应快速返回，实际耗时 %s", elapsed)
	}
	var fe *failure.Error
	if !errors.As(err, &fe) || fe.Class != failure.ClassConnection {
		t.Fatalf("超时应映射 ClassConnection，得到 %v", err)
	}
}

// 响应超上限：客户端熔断为 host_gone（reader 因帧超限关闭客户端），不影响别的 Host。
func TestHostResponseTooLarge(t *testing.T) {
	h, _ := newTestHost(&sampleHandlers{mode: "big"}, func(c *apphost.HostConfig) {
		c.MaxResponseBytes = 8 * 1024 // 8KiB 上限，64KiB 结果必超
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	defer h.Close()

	_, _, err := h.InvokeTool(sampleTool, nil)
	if err == nil {
		t.Fatal("超大响应应导致调用失败（熔断）")
	}
	// 帧超限使 reader 退出 → 之后的调用 host_gone。
	deadline := time.Now().Add(2 * time.Second)
	for h.State() != apphost.StateFailed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.State() != apphost.StateFailed {
		t.Fatalf("超大帧应使 Host 失败，得到 %s", h.State())
	}
}

// 子进程崩溃（管道关闭）→ Host 转 Failed，后续调用 host_gone，不影响别的 Host。
func TestHostCrashIsolation(t *testing.T) {
	h, fp := newTestHost(&sampleHandlers{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}
	_ = fp.toHostW.Close() // 模拟子进程退出
	deadline := time.Now().Add(2 * time.Second)
	for h.State() != apphost.StateFailed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.State() != apphost.StateFailed {
		t.Fatalf("子进程崩溃后 Host 应变 Failed，得到 %s", h.State())
	}
	_, _, err := h.InvokeTool(sampleTool, nil)
	if err == nil {
		t.Fatal("崩溃后调用应失败")
	}
	var fe *failure.Error
	if !errors.As(err, &fe) || fe.Code != apphost.ErrCodeHostGone {
		t.Fatalf("崩溃后应 host_gone，得到 %v", err)
	}
}

// 两个 Host 相互独立：一个崩溃不影响另一个。
func TestHostsAreIndependent(t *testing.T) {
	h1, fp1 := newTestHost(&sampleHandlers{}, nil)
	h2, _ := newTestHost(&sampleHandlers{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h1.Start(ctx); err != nil {
		t.Fatalf("h1 Start 失败：%v", err)
	}
	if err := h2.Start(ctx); err != nil {
		t.Fatalf("h2 Start 失败：%v", err)
	}
	defer h1.Close()
	defer h2.Close()

	_ = fp1.toHostW.Close()
	deadline := time.Now().Add(2 * time.Second)
	for h1.State() != apphost.StateFailed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h2.State() != apphost.StateReady {
		t.Fatalf("h1 崩溃不应影响 h2，h2 状态 %s", h2.State())
	}
	if _, _, err := h2.InvokeTool(sampleTool, nil); err != nil {
		t.Fatalf("h2 调用应仍成功：%v", err)
	}
}

// 错误码 → failure 分类映射（纯函数单测）。
func TestClassOfCode(t *testing.T) {
	cases := map[string]failure.Class{
		apphost.ErrCodeUnknownTool:        failure.ClassRequest,
		apphost.ErrCodeToolFailed:         failure.ClassService,
		apphost.ErrCodeAPIVersionMismatch: failure.ClassUnsupported,
		apphost.ErrCodeTimeout:            failure.ClassConnection,
		apphost.ErrCodeHostGone:           failure.ClassConnection,
		apphost.ErrCodeResponseTooLarge:   failure.ClassConnection,
		"totally_unknown":                 failure.ClassUnknown,
	}
	for code, want := range cases {
		if got := apphost.ClassOfCode(code); got != want {
			t.Errorf("ClassOfCode(%q)=%s want %s", code, got, want)
		}
	}
	// host 建议的 class 优先（合法时）；脏值回退按 code 推断。
	if got := apphost.ClassOfRPCError(&apphost.RPCError{Code: apphost.ErrCodeToolFailed, Class: "unsupported"}); got != failure.ClassUnsupported {
		t.Errorf("合法建议 class 应优先，得到 %s", got)
	}
	if got := apphost.ClassOfRPCError(&apphost.RPCError{Code: apphost.ErrCodeToolFailed, Class: "bogus"}); got != failure.ClassService {
		t.Errorf("脏 class 应回退按 code 推断，得到 %s", got)
	}
}
