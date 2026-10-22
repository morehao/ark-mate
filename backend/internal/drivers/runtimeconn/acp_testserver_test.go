package runtimeconn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"

	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
)

// testACPResponder 是 runtimeconn 测试用的**自建最小 ACP responder**。
//
// 为什么需要它：`TestConnectRemoteTransport` 原先拿内置 echo 底座当对端（echo 已于 M0 删除）。
// 若一并删掉该测试，本包 `connect` 的远程分支（ws/http）就失去覆盖，而远程 runtime 是
// 受支持的产品形态（`runtimes.json` 可声明远端 ACP 端点）。这里照 `internal/infra/protocol/acp/remote_test.go`
// 的思路重建一份——那份 `remoteAgent` 在 `package acp` 的测试文件里，**跨包不可用**，必须重写。
//
// 两端都用真 SDK：agent 侧是 `acpsdk.NewAgentSideConnection`，客户端侧是 `acp.Dial`（内部同样
// 是真 SDK 客户端），服务端半场复用生产实现 `acp.ServeWSSession` / `acp.NewRemoteHandler`。
// 因此被测的是「传输适配 + Connect 装配」，协议语义完全由 SDK 保证，不是自造的协议。
type testACPResponder struct {
	mu   sync.Mutex
	conn *acpsdk.AgentSideConnection

	// sessionID NewSession 返回的会话 id。
	sessionID string
	// updates Prompt 期间先推送的流式更新。
	updates []acpsdk.SessionUpdate
	// cfgOpts NewSession 响应携带的会话配置项（模型目录面）。
	cfgOpts []acpsdk.SessionConfigOption
	// promptHook 若非 nil，在推送流式更新前调用（用于在 Prompt 中途发起权限请求等交互）。
	promptHook func(ctx context.Context, r *testACPResponder, req acpsdk.PromptRequest)
	// stopReason Prompt 的收尾原因（零值 → end_turn）。
	stopReason acpsdk.StopReason
	// promptErr 非 nil 时 Prompt 直接返回错误（失败轮）。
	promptErr error

	promptTexts []string
	cancelCalls int
	closedCalls int

	// permResult 权限请求的结果（`selected:<id>` / `cancelled` / `err: ...`），缓冲 4，
	// 由 promptHook 写入、测试侧读取（跨 goroutine 用通道而不是裸字段）。
	permResult chan string
}

var _ acpsdk.Agent = (*testACPResponder)(nil)

func newTestACPResponder() *testACPResponder {
	return &testACPResponder{
		sessionID:  "sess-responder-1",
		stopReason: acpsdk.StopReasonEndTurn,
		permResult: make(chan string, 4),
	}
}

func (r *testACPResponder) setConn(c *acpsdk.AgentSideConnection) {
	r.mu.Lock()
	r.conn = c
	r.mu.Unlock()
}

func (r *testACPResponder) connection() *acpsdk.AgentSideConnection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

func (r *testACPResponder) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

func (r *testACPResponder) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentCapabilities: acpsdk.AgentCapabilities{
			SessionCapabilities: acpsdk.SessionCapabilities{
				List:   &acpsdk.SessionListCapabilities{},
				Resume: &acpsdk.SessionResumeCapabilities{},
			},
		},
	}, nil
}

func (r *testACPResponder) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}

func (r *testACPResponder) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: acpsdk.SessionId(r.sessionID), ConfigOptions: r.cfgOpts}, nil
}

func (r *testACPResponder) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	r.mu.Lock()
	if len(req.Prompt) > 0 && req.Prompt[0].Text != nil {
		r.promptTexts = append(r.promptTexts, req.Prompt[0].Text.Text)
	}
	hook, updates, stop, perr := r.promptHook, r.updates, r.stopReason, r.promptErr
	r.mu.Unlock()

	if hook != nil {
		hook(ctx, r, req)
	}
	if perr != nil {
		return acpsdk.PromptResponse{}, perr
	}
	for _, u := range updates {
		if c := r.connection(); c != nil {
			_ = c.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: req.SessionId, Update: u})
		}
	}
	if stop == "" {
		stop = acpsdk.StopReasonEndTurn
	}
	return acpsdk.PromptResponse{StopReason: stop}, nil
}

func (r *testACPResponder) Cancel(context.Context, acpsdk.CancelNotification) error {
	r.mu.Lock()
	r.cancelCalls++
	r.mu.Unlock()
	return nil
}

func (r *testACPResponder) CloseSession(context.Context, acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	r.mu.Lock()
	r.closedCalls++
	r.mu.Unlock()
	return acpsdk.CloseSessionResponse{}, nil
}

func (r *testACPResponder) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, nil
}

func (r *testACPResponder) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return acpsdk.ResumeSessionResponse{ConfigOptions: r.cfgOpts}, nil
}

func (r *testACPResponder) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

func (r *testACPResponder) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: r.cfgOpts}, nil
}

// requestPermission 由 promptHook 调用：向客户端发起一次权限请求并返回结果的字符串表示
// （`selected:<optionId>` / `cancelled` / `err: ...`）。
func (r *testACPResponder) requestPermission(ctx context.Context, sessionID acpsdk.SessionId) string {
	c := r.connection()
	if c == nil {
		return "no-conn"
	}
	resp, err := c.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc-responder", Title: ptrStr("responder 工具调用")},
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow-once", Name: "允许一次", Kind: acpsdk.PermissionOptionKindAllowOnce},
			{OptionId: "allow-all", Name: "始终允许", Kind: acpsdk.PermissionOptionKindAllowAlways},
		},
	})
	if err != nil {
		return "err: " + err.Error()
	}
	if resp.Outcome.Selected != nil {
		return "selected:" + string(resp.Outcome.Selected.OptionId)
	}
	if resp.Outcome.Cancelled != nil {
		return "cancelled"
	}
	return "unknown"
}

func ptrStr(s string) *string { return &s }

// ——— 服务端半场（复用生产实现 acp.ServeWSSession / acp.NewRemoteHandler） ———

// serveResponderWS 起一个真实 ACP server 挂在 WS 端点 /acp 上，返回 ws:// URL。
func serveResponderWS(t *testing.T, r *testACPResponder) string {
	t.Helper()
	opts := acp.RemoteServerOptions{Serve: func(peerInput io.Writer, peerOutput io.Reader) {
		r.setConn(acpsdk.NewAgentSideConnection(r, peerInput, peerOutput))
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/acp", func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		_ = acp.ServeWSSession(ctx, conn, opts)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/acp"
}

// serveResponderHTTP 起一个真实 ACP server（Streamable HTTP 形态），返回 http:// URL。
func serveResponderHTTP(t *testing.T, r *testACPResponder) string {
	t.Helper()
	opts := acp.RemoteServerOptions{
		Serve: func(peerInput io.Writer, peerOutput io.Reader) {
			r.setConn(acpsdk.NewAgentSideConnection(r, peerInput, peerOutput))
		},
		SessionIDPrefix: "sess-http",
	}
	srv := httptest.NewServer(acp.NewRemoteHandler(opts))
	t.Cleanup(srv.Close)
	return srv.URL + "/acp"
}
