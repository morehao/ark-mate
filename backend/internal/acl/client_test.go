package acl

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ——— 内存双管道 Transport（免真实子进程，验证 Dial 全链路） ———

// pipeTransport 模拟 stdio 传输层：两对 io.Pipe 连通「客户端 ↔ agent」。
//   - c2a：客户端写（Stdin）→ agent 读
//   - a2c：agent 写 → 客户端读（Stdout）
type pipeTransport struct {
	mu      sync.Mutex
	c2aW    *io.PipeWriter
	c2aR    *io.PipeReader
	a2cW    *io.PipeWriter
	a2cR    *io.PipeReader
	started bool
	closed  bool
}

func newPipeTransport() *pipeTransport {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	return &pipeTransport{c2aW: c2aW, c2aR: c2aR, a2cW: a2cW, a2cR: a2cR}
}

func (t *pipeTransport) Start(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	return nil
}

func (t *pipeTransport) Stdin() io.Writer {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return nil
	}
	return t.c2aW
}

func (t *pipeTransport) Stdout() io.Reader {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return nil
	}
	return t.a2cR
}

func (t *pipeTransport) StderrTail() []string { return nil }

func (t *pipeTransport) Close() error {
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

// ——— 假 ACP agent（实现 acp.Agent，可编程 initialize/prompt/记录调用） ———

type fakeAgent struct {
	conn *acpsdk.AgentSideConnection

	init acpsdk.InitializeResponse

	mu            sync.Mutex
	authCalls     int
	newCalls      []acpsdk.NewSessionRequest
	promptTexts   []string
	cancelCalls   int
	closeCalls    int
	resumeCalls   int
	listCalls     int
	promptErr     error
	promptUpdates []acpsdk.SessionUpdate // Prompt 时先推送的流式更新
	stopReason    acpsdk.StopReason
}

var _ acpsdk.Agent = (*fakeAgent)(nil)

func (f *fakeAgent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	f.mu.Lock()
	f.authCalls++
	f.mu.Unlock()
	return acpsdk.AuthenticateResponse{}, nil
}

func (f *fakeAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return f.init, nil
}

func (f *fakeAgent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}

func (f *fakeAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	f.mu.Lock()
	f.cancelCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeAgent) CloseSession(context.Context, acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return acpsdk.CloseSessionResponse{}, nil
}

func (f *fakeAgent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	f.mu.Lock()
	f.listCalls++
	f.mu.Unlock()
	return acpsdk.ListSessionsResponse{}, nil
}

func (f *fakeAgent) NewSession(_ context.Context, req acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	f.mu.Lock()
	f.newCalls = append(f.newCalls, req)
	f.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: "sess-fake-1"}, nil
}

func (f *fakeAgent) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	f.mu.Lock()
	if len(req.Prompt) > 0 && req.Prompt[0].Text != nil {
		f.promptTexts = append(f.promptTexts, req.Prompt[0].Text.Text)
	}
	updates := f.promptUpdates
	stop := f.stopReason
	err := f.promptErr
	f.mu.Unlock()
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	// 先推送流式更新（通知），再返回终态：验证客户端先收增量、后收 stop reason。
	for _, u := range updates {
		if f.conn != nil {
			_ = f.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: req.SessionId, Update: u})
		}
	}
	if stop == "" {
		stop = acpsdk.StopReasonEndTurn
	}
	return acpsdk.PromptResponse{StopReason: stop}, nil
}

func (f *fakeAgent) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	f.mu.Lock()
	f.resumeCalls++
	f.mu.Unlock()
	return acpsdk.ResumeSessionResponse{}, nil
}

func (f *fakeAgent) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

func (f *fakeAgent) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, nil
}

func (f *fakeAgent) stats() (auth, cancel, closeCalls, resume, list int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authCalls, f.cancelCalls, f.closeCalls, f.resumeCalls, f.listCalls
}

// fakeInit 构造 initialize 响应。
func fakeInit(protocol int, caps acpsdk.AgentCapabilities, auth []acpsdk.AuthMethod) acpsdk.InitializeResponse {
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersion(protocol),
		AgentCapabilities: caps,
		AuthMethods:       auth,
	}
}

// dialCaps 便捷构造带会话能力的 AgentCapabilities。
func dialCaps(list, resume, closeCap bool) acpsdk.AgentCapabilities {
	sc := acpsdk.SessionCapabilities{}
	if list {
		sc.List = &acpsdk.SessionListCapabilities{}
	}
	if resume {
		sc.Resume = &acpsdk.SessionResumeCapabilities{}
	}
	if closeCap {
		sc.Close = &acpsdk.SessionCloseCapabilities{}
	}
	return acpsdk.AgentCapabilities{SessionCapabilities: sc}
}

// wireFake 建立内存 ACP 连接：agent 侧已就绪并自动开始读；返回的 transport 供 Dial。
func wireAgent(agent acpsdk.Agent) (*pipeTransport, *acpsdk.AgentSideConnection) {
	tp := newPipeTransport()
	asc := acpsdk.NewAgentSideConnection(agent, tp.a2cW, tp.c2aR)
	return tp, asc
}

func wireFake(agent *fakeAgent) *pipeTransport {
	tp, asc := wireAgent(agent)
	agent.conn = asc
	return tp
}

// dialFake 拨一条到假 agent 的连接（完整 initialize/authenticate 握手）。
// 测试结束自动 Close。
func dialFake(t *testing.T, agent *fakeAgent, opts ClientOptions) *Client {
	t.Helper()
	tp := wireFake(agent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// hangAgent 不响应 initialize（模拟 runtime 只起进程不答协议，如卡在交互登录）。
// 其他 Agent 方法走内嵌的空接口实现（本次拨号不会触达）。
type hangAgent struct {
	acpsdk.Agent
}

var _ acpsdk.Agent = (*hangAgent)(nil)

func (h *hangAgent) Initialize(ctx context.Context, _ acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	<-ctx.Done()
	return acpsdk.InitializeResponse{}, ctx.Err()
}

// TestDialHandshakeTimeout runtime 不答 initialize → Dial 在限期内返回 connection/timeout
// （此前会无限挂起，REST 创建会话随之卡死）。
func TestDialHandshakeTimeout(t *testing.T) {
	tp, _ := wireAgent(&hangAgent{})
	defer tp.Close()
	start := time.Now()
	_, err := Dial(context.Background(), tp, ClientOptions{DialTimeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("应因握手超时失败")
	}
	ae, _ := As(err)
	if ae == nil || ae.Class != ClassConnection || ae.Code != "timeout" {
		t.Fatalf("期望 connection/timeout，got %v (%v)", ae, err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("拨号未按 DialTimeout 返回: %v", el)
	}
}

// ——— 测试 ———

// TestDialAgentAuthAndCaps 带 agent 自托管鉴权的完整拨号：握手→能力冻结→Authenticate。
func TestDialAgentAuthAndCaps(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), []acpsdk.AuthMethod{
		{Agent: &acpsdk.AuthMethodAgent{Id: "self-hosted-id"}},
	})}
	cl := dialFake(t, agent, ClientOptions{})

	caps := cl.Capabilities()
	if !caps.SessionsPersistent || !caps.MultiSessionConcurrent {
		t.Fatalf("能力折叠不符: %+v", caps)
	}
	if len(caps.AuthMethods) != 1 || caps.AuthMethods[0] != "self-hosted-id" {
		t.Fatalf("AuthMethods 不符: %v", caps.AuthMethods)
	}
	if auth, _, _, _, _ := agent.stats(); auth != 1 {
		t.Fatalf("Authenticate 应被调用一次，got %d", auth)
	}
	if cl.Done() == nil {
		t.Fatal("Done 通道不应为 nil")
	}
}

// TestDialNoAuthNoAuthenticate authMethods 为空 → 不调用 Authenticate。
func TestDialNoAuthNoAuthenticate(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, false, false), nil)}
	dialFake(t, agent, ClientOptions{})
	if auth, _, _, _, _ := agent.stats(); auth != 0 {
		t.Fatalf("无鉴权要求时不应调用 Authenticate，got %d", auth)
	}
}

// TestDialProtocolMismatch 版本不一致 → unsupported/protocol_mismatch 且回收传输。
func TestDialProtocolMismatch(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(999, dialCaps(false, false, false), nil)}
	rig := wireFake(agent)
	_, err := Dial(context.Background(), rig, ClientOptions{})
	if err == nil {
		t.Fatal("期望协议不匹配错误")
	}
	ae, ok := As(err)
	if !ok || ae.Class != ClassUnsupported || ae.Code != "protocol_mismatch" {
		t.Fatalf("分类不符: %v", err)
	}
}

// TestDialAuthRequired 仅 env_var/terminal 鉴权（M0 不支持）→ access/auth_required。
func TestDialAuthRequired(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, false, false), []acpsdk.AuthMethod{
		{EnvVar: &acpsdk.AuthMethodEnvVarInline{}},
	})}
	rig := wireFake(agent)
	_, err := Dial(context.Background(), rig, ClientOptions{})
	if err == nil {
		t.Fatal("期望 auth_required")
	}
	ae, ok := As(err)
	if !ok || ae.Class != ClassAccess || ae.Code != "auth_required" {
		t.Fatalf("分类不符: %v", err)
	}
}

// TestClientNewSessionRoundTrip NewSession（cwd 透传）→ Prompt（增量事件先到，再返回 stop）→ Cancel。
func TestClientNewSessionRoundTrip(t *testing.T) {
	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
		promptUpdates: []acpsdk.SessionUpdate{
			acpsdk.UpdateAgentMessageText("hello "),
			acpsdk.UpdateAgentThoughtText("thinking"),
			acpsdk.UpdateAgentMessageText("world"),
		},
	}
	events := make(chan Event, 32)
	cl := dialFake(t, agent, ClientOptions{OnEvent: func(e Event) { events <- e }})

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID() != "sess-fake-1" {
		t.Fatalf("session id 不符: %s", sess.ID())
	}
	if sess.Client() != cl {
		t.Fatal("Session.Client 应回指连接")
	}

	stop, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop reason 不符: %q", stop)
	}

	// 增量事件与顺序（message → thought → message）。
	got := drainEvents(events, 3)
	want := []struct{ kind, text string }{
		{KindMessage, "hello "},
		{KindThought, "thinking"},
		{KindMessage, "world"},
	}
	for i, w := range want {
		if len(got) <= i {
			t.Fatalf("事件不足: got %d want %d", len(got), len(want))
		}
		if got[i].Kind != w.kind {
			t.Fatalf("事件 %d kind 不符: got %s want %s", i, got[i].Kind, w.kind)
		}
	}
	if d, ok := got[0].Payload.(MessageDelta); !ok || d.Text != "hello " {
		t.Fatalf("文本增量不符: %+v", got[0].Payload)
	}

	if err := sess.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// Cancel 是通知类调用，agent 侧异步处理：轮询等待送达。
	eventually(t, "cancel 送达 agent", 2*time.Second, func() bool {
		_, cancel, _, _, _ := agent.stats()
		return cancel == 1
	})

	// caps 无 close → Close 是本地 no-op。
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, closeCalls, _, _ := agent.stats(); closeCalls != 0 {
		t.Fatalf("无 close 能力不应发出 wire 调用")
	}

	// cwd 透传核对。
	if len(agent.newCalls) != 1 || agent.newCalls[0].Cwd != "/work" {
		t.Fatalf("NewSession 参数不符: %+v", agent.newCalls)
	}
	if len(agent.promptTexts) != 1 || agent.promptTexts[0] != "hi" {
		t.Fatalf("Prompt 文本不符: %v", agent.promptTexts)
	}
}

// TestSessionCloseWithCapability 声明 close 能力 → Close 走 wire。
func TestSessionCloseWithCapability(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, false, true), nil)}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, closeCalls, _, _ := agent.stats(); closeCalls != 1 {
		t.Fatalf("声明 close 能力时应发出 wire close")
	}
}

// TestCapabilityGatedMethods load/resume/list 能力门：未声明 → 本地 unsupported（不发 wire）。
func TestCapabilityGatedMethods(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, false, false), nil)}
	cl := dialFake(t, agent, ClientOptions{})

	if _, err := cl.LoadSession(context.Background(), "x", SessionOptions{}); err == nil {
		t.Fatal("load 未声明应报 unsupported")
	} else if ae, ok := As(err); !ok || ae.Code != "load_session_unsupported" {
		t.Fatalf("load 错误不符: %v", err)
	}
	if _, err := cl.ResumeSession(context.Background(), "x", SessionOptions{}); err == nil {
		t.Fatal("resume 未声明应报 unsupported")
	} else if ae, ok := As(err); !ok || ae.Code != "resume_session_unsupported" {
		t.Fatalf("resume 错误不符: %v", err)
	}
	if _, err := cl.ListSessions(context.Background()); err == nil {
		t.Fatal("list 未声明应报 unsupported")
	} else if ae, ok := As(err); !ok || ae.Code != "list_sessions_unsupported" {
		t.Fatalf("list 错误不符: %v", err)
	}

	_, cancel, closeCalls, resume, list := agent.stats()
	if cancel != 0 || closeCalls != 0 || resume != 0 || list != 0 {
		t.Fatalf("能力门应拦截 wire 调用: cancel=%d close=%d resume=%d list=%d", cancel, closeCalls, resume, list)
	}
}

// TestClientCloseIdempotent Close 幂等。
func TestClientCloseIdempotent(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, false, false), nil)}
	rig := wireFake(agent)
	cl, err := Dial(context.Background(), rig, ClientOptions{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := cl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := cl.Close(); err != nil {
		t.Fatalf("二次 Close 应幂等: %v", err)
	}
}

// drainEvents 从 channel 收满 n 个事件（带超时，防挂测）。
func drainEvents(ch chan Event, n int) []Event {
	out := make([]Event, 0, n)
	timeout := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-timeout:
			return out
		}
	}
	return out
}

// eventually 轮询等待条件成立（异步通知送达的轻量同步点）。
func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestStdioTransportStderrRing stderr 环形缓冲保留最近行（真实 sh 子进程）。
func TestStdioTransportStderrRing(t *testing.T) {
	tp := NewStdioTransport("sh", []string{"-c", "echo err1 >&2; echo err2 >&2; sleep 0.2"}, nil)
	if err := tp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	tail := tp.StderrTail()
	if len(tail) < 2 || tail[len(tail)-1] != "err2" {
		t.Fatalf("stderr 环形缓冲不符: %v", tail)
	}
	if err := tp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestStdioTransportCloseKillsProcess Close 后子进程被回收：写 stdin 报错。
func TestStdioTransportCloseKillsProcess(t *testing.T) {
	tp := NewStdioTransport("sh", []string{"-c", "cat"}, nil)
	if err := tp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w := tp.Stdin()
	if w == nil {
		return // Close 后已置空（部分实现）
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("Close 后写 stdin 应失败")
	} else if !errors.Is(err, io.ErrClosedPipe) {
		_ = err // 管道写失败（EPIPE/closed）均可接受
	}
}
