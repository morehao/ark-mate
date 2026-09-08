package runtime

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	echort "github.com/morehao/ark-forge/internal/runtime/echo"
)

// inProcEchoTransport 把 echo.Serve（进程内演示底座）接到 acl.Transport 协议面
// （语义镜像 acl 测试的 pipeTransport）：io.Pipe 两对——host 写 c2aW → agent 读 c2aR；
// agent 写 a2cW → host 读 a2cR。Serve 于构造时即启动（agent 侧常驻读循环），
// Start 仅置位（acl.Dial 要求 Start 后才提供 Stdin/Stdout）。
type inProcEchoTransport struct {
	mu      sync.Mutex
	c2aW    *io.PipeWriter
	c2aR    *io.PipeReader
	a2cW    *io.PipeWriter
	a2cR    *io.PipeReader
	started bool
	closed  bool
}

func newInProcEchoTransport() *inProcEchoTransport {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	t := &inProcEchoTransport{c2aW: c2aW, c2aR: c2aR, a2cW: a2cW, a2cR: a2cR}
	echort.Serve(a2cW, c2aR)
	return t
}

func (t *inProcEchoTransport) Start(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	return nil
}
func (t *inProcEchoTransport) Stdin() io.Writer {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return nil
	}
	return t.c2aW
}
func (t *inProcEchoTransport) Stdout() io.Reader {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return nil
	}
	return t.a2cR
}
func (t *inProcEchoTransport) StderrTail() []string { return nil }
func (t *inProcEchoTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	// 关写端 → 对端读到 EOF → 连接收尾。
	_ = t.c2aW.Close()
	_ = t.a2cW.Close()
	return nil
}

// TestEchoRuntimeExecutableProjection 验证 Runtime 接口的 echo 投影可执行（§4.2 会话边界版）：
// 进程内真实 EchoAgent 上完成 Dial → NewSession → Prompt×2（多轮，权限钩子自动放行）→
// 接口字段与不支持操作契约。无任何子进程/外部依赖。
func TestEchoRuntimeExecutableProjection(t *testing.T) {
	tp := newInProcEchoTransport()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := acl.Dial(ctx, tp, acl.ClientOptions{
		RequestPermission: func(_ context.Context, _ acl.PermissionRequest) acl.PermissionDecision {
			return acl.PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = cl.Close()
		_ = tp.Close()
	}()

	sess, err := cl.NewSession(context.Background(), acl.SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	rt := NewEchoRuntime(sess)
	if rt.ID() != "echo" {
		t.Fatalf("ID 不符: %q", rt.ID())
	}
	if !rt.ManagesOwnContext() {
		t.Fatal("echo 投影应声明 ManagesOwnContext=true")
	}
	if rt.ProviderSessionID() != sess.ID() {
		t.Fatalf("ProviderSessionID 不符: %q != %q", rt.ProviderSessionID(), sess.ID())
	}

	// 第一轮（含权限 round，钩子自动放行）。
	stop, err := rt.Prompt(ctx, PromptRequest{Text: "帮我跑个演示"})
	if err != nil {
		t.Fatalf("Prompt 第一轮: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop reason 不符: %q", stop)
	}
	// 第二轮（多轮可续）。
	stop, err = rt.Prompt(ctx, PromptRequest{Text: "再跑一次"})
	if err != nil || stop != "end_turn" {
		t.Fatalf("Prompt 第二轮: stop=%q err=%v", stop, err)
	}

	// 不支持操作契约：显式报错，不静默吞掉。
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"ApplyMode", func() error { return rt.ApplyMode(ctx, ModeAuto) }},
		{"ApplyModel", func() error { return rt.ApplyModel(ctx, ModelConfig{Model: "any"}) }},
		{"Resume", func() error { return rt.Resume(ctx, "other-session") }},
	} {
		if err := tc.fn(); err == nil {
			t.Fatalf("%s 应返回不支持错误", tc.name)
		} else if ae, ok := acl.As(err); !ok || ae.Code == "" {
			t.Fatalf("%s 应为 acl 错误且带 code: %v", tc.name, err)
		}
	}

	// Cancel 契约（§17 E2：会话级取消经投影；echo 的 Cancel handler 为 nil 成功）。
	if err := rt.Cancel(ctx); err != nil {
		t.Fatalf("Cancel 应成功: %v", err)
	}
	// Resume 实测守卫（2026-09-08）：echo 未声明 sessionCapabilities.resume →
	// acl.ResumeSession 在能力门被拒（resume_session_unsupported，wire 前），不会发到 agent。
	if _, err := cl.ResumeSession(ctx, sess.ID(), acl.SessionOptions{Cwd: "/work"}); err == nil {
		t.Fatal("echo 未声明 resume 能力，ResumeSession 应被拒")
	} else if ae, ok := acl.As(err); !ok || ae.Code != "resume_session_unsupported" {
		t.Fatalf("期望 resume_session_unsupported，got %v", err)
	}
}

// TestThreeProjectionConstructors 验证 opencode / deepseekharness / echo 三投影各自携带
// §6.2 Spec 身份（ID/ManagesOwnContext），共用同一「活会话」实现。
func TestThreeProjectionConstructors(t *testing.T) {
	tp := newInProcEchoTransport()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := acl.Dial(ctx, tp, acl.ClientOptions{
		RequestPermission: func(_ context.Context, _ acl.PermissionRequest) acl.PermissionDecision {
			return acl.PermissionDecision{Allowed: true}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = cl.Close()
		_ = tp.Close()
	}()

	sess, err := cl.NewSession(context.Background(), acl.SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// 三个投影共享同一活会话做身份断言（ID/自管上下文取自各自 Spec）。
	for _, tc := range []struct {
		id     string
		build  func(*acl.Session) Runtime
		ownCtx bool
	}{
		{"echo", func(s *acl.Session) Runtime { return NewEchoRuntime(s) }, true},
		{"opencode", func(s *acl.Session) Runtime { return NewOpencodeRuntime(s) }, true},
		{"deepseekharness", func(s *acl.Session) Runtime { return NewDeepseekHarnessRuntime(s) }, true},
	} {
		rt := tc.build(sess)
		if rt.ID() != tc.id {
			t.Fatalf("%s 投影 ID 不符: %q", tc.id, rt.ID())
		}
		if rt.ManagesOwnContext() != tc.ownCtx {
			t.Fatalf("%s 投影 ManagesOwnContext 不符: %v", tc.id, rt.ManagesOwnContext())
		}
		if rt.ProviderSessionID() != sess.ID() {
			t.Fatalf("%s 投影 ProviderSessionID 不符", tc.id)
		}
		// 模型切换需 env/provider 配置路径（ModelConfigOptionID 空）→ 不支持错误。
		if err := rt.ApplyModel(ctx, ModelConfig{Model: "x"}); err == nil {
			t.Fatalf("%s ApplyModel 应返回不支持错误", tc.id)
		}
	}
}

// TestNewProjectionDispatcher 分发器（Orchestrator 采用接缝）：三投影按 Spec.ID 路由、
// 未知 id 返回 nil；构造不触碰会话（nil sess 亦返回非 nil 实例，方法调用方负责非 nil）。
func TestNewProjectionDispatcher(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"echo", true},
		{"opencode", true},
		{"deepseekharness", true},
		{"unknown-rt", false},
	} {
		spec := Spec{ID: tc.id}
		rt := NewProjection(spec, nil)
		if tc.want {
			if rt == nil || rt.ID() != tc.id {
				t.Fatalf("%s 应分发到投影（ID=%s），got %+v", tc.id, tc.id, rt)
			}
			if !rt.ManagesOwnContext() {
				t.Fatalf("%s 投影应自管上下文", tc.id)
			}
		} else if rt != nil {
			t.Fatalf("未知 runtime %q 应返回 nil，got %+v", tc.id, rt)
		}
	}
}
