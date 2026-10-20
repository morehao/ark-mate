package acp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"
)

// ——— A5 远程传输端到端测试的「真实 ACP server」 ——
//
// 两端都用真 SDK：本文件里的 server 侧是 acpsdk.NewAgentSideConnection，客户端侧是
// acp.Dial（内部同样是 acpsdk.NewClientSideConnection）。传输层换的只是「字节怎么走」，
// 协议语义完全由 SDK 保证——这样测的是传输适配器，而不是自造的协议。

// remoteAgent 是测试用 ACP agent（比 fakeAgent 多一层 conn 互斥，避免 -race 下
// handler 写 conn 与 promptHook 读 conn 竞争）。
type remoteAgent struct {
	mu         sync.Mutex
	conn       *acpsdk.AgentSideConnection
	promptHook func(ctx context.Context, a *remoteAgent, req acpsdk.PromptRequest)
	updates    []acpsdk.SessionUpdate

	newCalls    int
	promptTexts []string
	cancelCalls int
	permResult  chan string // 权限请求的结果（selected option / cancelled）
}

var _ acpsdk.Agent = (*remoteAgent)(nil)

func (a *remoteAgent) setConn(c *acpsdk.AgentSideConnection) {
	a.mu.Lock()
	a.conn = c
	a.mu.Unlock()
}

func (a *remoteAgent) connection() *acpsdk.AgentSideConnection {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

func (a *remoteAgent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

func (a *remoteAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		AgentCapabilities: dialCaps(true, true, false),
	}, nil
}

func (a *remoteAgent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}

func (a *remoteAgent) NewSession(_ context.Context, _ acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	a.mu.Lock()
	a.newCalls++
	a.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: "sess-remote-1"}, nil
}

func (a *remoteAgent) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	a.mu.Lock()
	if len(req.Prompt) > 0 && req.Prompt[0].Text != nil {
		a.promptTexts = append(a.promptTexts, req.Prompt[0].Text.Text)
	}
	hook, updates := a.promptHook, a.updates
	a.mu.Unlock()
	if hook != nil {
		hook(ctx, a, req)
	}
	for _, u := range updates {
		if c := a.connection(); c != nil {
			_ = c.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: req.SessionId, Update: u})
		}
	}
	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

func (a *remoteAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	a.mu.Lock()
	a.cancelCalls++
	a.mu.Unlock()
	return nil
}

func (a *remoteAgent) CloseSession(context.Context, acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	return acpsdk.CloseSessionResponse{}, nil
}

func (a *remoteAgent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, nil
}

func (a *remoteAgent) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, nil
}

func (a *remoteAgent) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

func (a *remoteAgent) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, nil
}

// requestPermission 由 promptHook 调用：向客户端发起一次权限请求并把结果回传通道。
func (a *remoteAgent) requestPermission(ctx context.Context, sessionID acpsdk.SessionId) {
	c := a.connection()
	if c == nil {
		a.permResult <- "no-conn"
		return
	}
	resp, err := c.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc-remote", Title: ptrString("远程工具调用")},
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow-once", Name: "允许一次", Kind: acpsdk.PermissionOptionKindAllowOnce},
			{OptionId: "allow-all", Name: "始终允许", Kind: acpsdk.PermissionOptionKindAllowAlways},
		},
	})
	if err != nil {
		a.permResult <- "err: " + err.Error()
		return
	}
	if resp.Outcome.Selected != nil {
		a.permResult <- "selected:" + string(resp.Outcome.Selected.OptionId)
		return
	}
	if resp.Outcome.Cancelled != nil {
		a.permResult <- "cancelled"
		return
	}
	a.permResult <- "unknown"
}

// ——— 真实 ACP server（服务端用生产实现 acp.ServeWSSession / acp.NewRemoteHandler） ———

// newWSACPServer 起一个真实 ACP server（agent 侧用 SDK）挂在 WS 端点 /acp 上，
// 服务端半场复用生产实现（acp.ServeWSSession），因此测的是「客户端传输 ↔ 服务端传输」闭环。
func newWSACPServer(t *testing.T, agent *remoteAgent) *httptest.Server {
	t.Helper()
	opts := RemoteServerOptions{Serve: func(peerInput io.Writer, peerOutput io.Reader) {
		agent.setConn(acpsdk.NewAgentSideConnection(agent, peerInput, peerOutput))
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/acp", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		_ = ServeWSSession(ctx, conn, opts)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newHTTPACPServer 起一个真实 ACP server（Streamable HTTP 形态，生产实现 acp.NewRemoteHandler）。
func newHTTPACPServer(t *testing.T, agent *remoteAgent) (*httptest.Server, *countingHandler) {
	t.Helper()
	opts := RemoteServerOptions{Serve: func(peerInput io.Writer, peerOutput io.Reader) {
		agent.setConn(acpsdk.NewAgentSideConnection(agent, peerInput, peerOutput))
	}, SessionIDPrefix: "sess-http"}
	h := NewRemoteHandler(opts)
	c := &countingHandler{inner: h, posts: &atomic.Int32{}, missingSession: &atomic.Int32{}}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return srv, c
}

// countingHandler 包一层统计 POST 次数与是否回带会话头（断言用）。
type countingHandler struct {
	inner          http.Handler
	posts          *atomic.Int32
	missingSession *atomic.Int32
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		n := c.posts.Add(1)
		if n > 1 && r.Header.Get(remoteSessionHeader) == "" {
			c.missingSession.Add(1)
		}
	}
	c.inner.ServeHTTP(w, r)
}

// drainStreamEvents 收 n 条「流式」事件（跳过权限/elicitation 等交互事件）。
func drainStreamEvents(ch chan Event, n int) []Event {
	out := make([]Event, 0, n)
	deadline := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case e := <-ch:
			if e.Kind == KindMessage || e.Kind == KindThought || e.Kind == KindToolCall {
				out = append(out, e)
			}
		case <-deadline:
			return out
		}
	}
	return out
}

// ——— 测试 ———

// TestWSTransportEndToEnd ws 传输打通真实 ACP 全链路：握手 → 建会话 → 流式更新 →
// Prompt 中途权限请求（服务端发起）→ 客户端决策回传。
func TestWSTransportEndToEnd(t *testing.T) {
	agent := &remoteAgent{
		updates: []acpsdk.SessionUpdate{
			acpsdk.UpdateAgentMessageText("ws-hello "),
			acpsdk.UpdateAgentThoughtText("ws-think"),
			acpsdk.UpdateAgentMessageText("ws-world"),
		},
		permResult: make(chan string, 1),
	}
	agent.promptHook = func(ctx context.Context, a *remoteAgent, req acpsdk.PromptRequest) {
		a.requestPermission(ctx, req.SessionId)
	}
	srv := newWSACPServer(t, agent)

	tr, err := NewRemoteTransport(RemoteSpec{
		Transport: "ws",
		Endpoint:  "ws" + strings.TrimPrefix(srv.URL, "http") + "/acp",
	})
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	events := make(chan Event, 16)
	permSeen := make(chan PermissionRequest, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tr, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, req PermissionRequest) PermissionDecision {
			permSeen <- req
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial(ws): %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID() != "sess-remote-1" {
		t.Fatalf("session id 不符: %s", sess.ID())
	}
	stop, err := sess.Prompt(context.Background(), "远程 ws 测试")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop reason 不符: %q", stop)
	}
	if got := drainStreamEvents(events, 3); len(got) != 3 ||
		got[0].Kind != KindMessage || got[1].Kind != KindThought || got[2].Kind != KindMessage {
		t.Fatalf("流式事件不符: %+v", got)
	}
	select {
	case pr := <-permSeen:
		if pr.ToolCall.Title == "" || len(pr.Options) != 2 {
			t.Fatalf("权限请求内容不符: %+v", pr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到权限请求（服务端发起 → 客户端）")
	}
	select {
	case res := <-agent.permResult:
		if res != "selected:allow-once" {
			t.Fatalf("权限决策回传不符: %s", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("权限决策未回传到 agent")
	}
	// 远程传输无子进程：pid 为零值（进程注册表/回收不适用）。
	if info := cl.PidInfo(); info.PID != 0 {
		t.Fatalf("远程传输不应报告 pid: %+v", info)
	}
	if tail := tr.StderrTail(); len(tail) != 0 {
		t.Fatalf("正常链路不应有诊断尾巴: %v", tail)
	}
}

// TestHTTPTransportEndToEnd Streamable HTTP 传输打通同一条链路，并核对：
// 响应头下发的 Acp-Session-Id 被后续请求回带；流上的非 JSON 噪声行被丢弃。
func TestHTTPTransportEndToEnd(t *testing.T) {
	agent := &remoteAgent{
		updates: []acpsdk.SessionUpdate{
			acpsdk.UpdateAgentMessageText("http-hello"),
			acpsdk.UpdateAgentThoughtText("http-think"),
			acpsdk.UpdateAgentMessageText("http-world"),
		},
		permResult: make(chan string, 1),
	}
	agent.promptHook = func(ctx context.Context, a *remoteAgent, req acpsdk.PromptRequest) {
		a.requestPermission(ctx, req.SessionId)
	}
	srv, counter := newHTTPACPServer(t, agent)

	tr, err := NewRemoteTransport(RemoteSpec{Transport: "http", Endpoint: srv.URL + "/acp"})
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	events := make(chan Event, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tr, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-all", Always: true}
		},
	})
	if err != nil {
		t.Fatalf("Dial(http): %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, err := sess.Prompt(context.Background(), "远程 http 测试")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop reason 不符: %q", stop)
	}
	if got := drainStreamEvents(events, 3); len(got) != 3 ||
		got[0].Kind != KindMessage || got[1].Kind != KindThought || got[2].Kind != KindMessage {
		t.Fatalf("流式事件不符: %+v", got)
	}
	select {
	case res := <-agent.permResult:
		if res != "selected:allow-all" {
			t.Fatalf("权限决策回传不符: %s", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("权限决策未回传到 agent")
	}
	if rt, ok := tr.(*remoteTransport); ok {
		if sid := rt.SessionID(); !strings.HasPrefix(sid, "sess-http-") {
			t.Fatalf("Acp-Session-Id 未捕获: %q", sid)
		}
	} else {
		t.Fatal("NewRemoteTransport 应返回 *remoteTransport")
	}
	if n := counter.missingSession.Load(); n != 0 {
		t.Fatalf("%d 个非首个 POST 未回带 Acp-Session-Id", n)
	}
	if counter.posts.Load() < 3 {
		t.Fatalf("POST 次数过少（每条客户端消息应一次 POST）: %d", counter.posts.Load())
	}
}

// TestRemoteTransportErrors 参数校验与连接失败路径（诊断尾巴必须可读）。
func TestRemoteTransportErrors(t *testing.T) {
	t.Run("不支持的传输", func(t *testing.T) {
		_, err := NewRemoteTransport(RemoteSpec{Transport: "grpc", Endpoint: "http://127.0.0.1:1"})
		if err == nil || Classify(err).Class != ClassUnsupported {
			t.Fatalf("应报 unsupported: %v", err)
		}
	})
	t.Run("scheme 与传输不匹配", func(t *testing.T) {
		if _, err := NewRemoteTransport(RemoteSpec{Transport: "ws", Endpoint: "http://127.0.0.1:1"}); err == nil {
			t.Fatal("ws + http:// 应报错")
		}
		if _, err := NewRemoteTransport(RemoteSpec{Transport: "http", Endpoint: "ws://127.0.0.1:1"}); err == nil {
			t.Fatal("http + ws:// 应报错")
		}
	})
	t.Run("端点非法", func(t *testing.T) {
		if _, err := NewRemoteTransport(RemoteSpec{Transport: "http", Endpoint: "not-a-url"}); err == nil {
			t.Fatal("非法 URL 应报错")
		}
	})
	t.Run("ws 连不上", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/nope"
		srv.Close() // 立即关闭 → 连接被拒
		tr, err := NewRemoteTransport(RemoteSpec{Transport: "ws", Endpoint: url, DialTimeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("NewRemoteTransport: %v", err)
		}
		if err := tr.Start(context.Background()); err == nil {
			t.Fatal("连不上应报错")
		} else if Classify(err).Class != ClassConnection {
			t.Fatalf("应归类 connection: %v", err)
		}
		if len(tr.StderrTail()) == 0 {
			t.Fatal("失败应留下诊断尾巴")
		}
	})
	t.Run("http 5xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
		defer srv.Close()
		tr, err := NewRemoteTransport(RemoteSpec{Transport: "http", Endpoint: srv.URL})
		if err != nil {
			t.Fatalf("NewRemoteTransport: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := Dial(ctx, tr, ClientOptions{}); err == nil {
			t.Fatal("5xx 应导致拨号失败")
		}
		tail := strings.Join(tr.StderrTail(), "\n")
		if !strings.Contains(tail, "502") || !strings.Contains(tail, "boom") {
			t.Fatalf("诊断尾巴应含状态与响应体: %q", tail)
		}
		// Close 幂等。
		if err := tr.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := tr.Close(); err != nil {
			t.Fatalf("Close 应幂等: %v", err)
		}
	})
}
