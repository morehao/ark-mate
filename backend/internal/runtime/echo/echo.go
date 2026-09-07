// Package echo 提供一个自包含的本地 ACP agent（echo 演示底座）：
// 每次 prompt 会发起一次真实的 session/request_permission（allow/reject × once/always），
// 用于在不依赖 opencode/deepseek-harness 与模型 key 的情况下，端到端验证权限确认通道
// （会话流 → 权限卡 → REST 决策 → 结果回流）。
//
// 它以子进程形态启动：`<forge 可执行文件> acp-demo`（与 opencode acp / dsh --profile acp 同构），
// 走 stdio NDJSON。父进程（manager.acquire）对它与其他 runtime 一视同仁。
package echo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// 注册表键与展示信息。
const (
	ID      = "echo"
	Name    = "echo"
	Version = "echo acp-demo（本地自包含权限演示）"
	Notes   = "自包含本地演示：每轮 prompt 发起一次真实权限确认（allow/reject × once/always），" +
		"用于端到端验证权限确认通道；无需模型 key / 外部安装。以 forge acp-demo 子进程形态提供 ACP server。"
)

// Args 是 serve/manager 拉起本 runtime 的子命令参数。
var Args = []string{"acp-demo"}

// Command 返回当前可执行文件路径（本 runtime 自派生：forge serve → forge acp-demo）。
func Command() (string, error) { return os.Executable() }

// RunServer 以 stdio 形态运行 ACP server，直至对端断开（父进程退出 → stdin EOF → 返回）。
// cmd/forge 的 acp-demo 子命令入口。
func RunServer() error {
	asc := Serve(os.Stdout, os.Stdin)
	<-asc.Done()
	return nil
}

// Serve 把 echoAgent 绑定到 peerInput(写向客户端)/peerOutput(读自客户端)，
// 构造并返回 AgentSideConnection（构造即开始读循环；调用方负责保持运行）。
func Serve(peerInput io.Writer, peerOutput io.Reader) *acpsdk.AgentSideConnection {
	a := New()
	asc := acpsdk.NewAgentSideConnection(a, peerInput, peerOutput)
	a.attach(asc)
	asc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return asc
}

// New 构造 echoAgent（未绑定连接前 Prompt 不会被调用）。
func New() *EchoAgent { return &EchoAgent{} }

// EchoAgent 实现 acpsdk.Agent（ACP server 侧）。
type EchoAgent struct {
	conn *acpsdk.AgentSideConnection
	n    atomic.Int64
}

func (a *EchoAgent) attach(asc *acpsdk.AgentSideConnection) { a.conn = asc }

// 权限请求的可选项（decision v4①：allow/reject × once/always）。
var permOptions = []acpsdk.PermissionOption{
	{OptionId: "allow-once", Name: "允许一次", Kind: acpsdk.PermissionOptionKindAllowOnce},
	{OptionId: "allow-all", Name: "始终允许", Kind: acpsdk.PermissionOptionKindAllowAlways},
	{OptionId: "reject-once", Name: "拒绝一次", Kind: acpsdk.PermissionOptionKindRejectOnce},
	{OptionId: "reject-all", Name: "始终拒绝", Kind: acpsdk.PermissionOptionKindRejectAlways},
}

func (a *EchoAgent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

func (a *EchoAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	// 无鉴权要求、无会话持久能力：M0 握手即最简形态（ProtocolVersion 对齐客户端）。
	return acpsdk.InitializeResponse{ProtocolVersion: acpsdk.ProtocolVersionNumber}, nil
}

func (a *EchoAgent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}

func (a *EchoAgent) Cancel(context.Context, acpsdk.CancelNotification) error { return nil }

func (a *EchoAgent) CloseSession(context.Context, acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	return acpsdk.CloseSessionResponse{}, nil
}

func (a *EchoAgent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, nil
}

func (a *EchoAgent) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	// 每次会话一个稳定 id（多会话/多轮在内存里自然成立）。
	return acpsdk.NewSessionResponse{
		SessionId: acpsdk.SessionId(fmt.Sprintf("echo-%d", time.Now().UnixNano())),
	}, nil
}

func (a *EchoAgent) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, nil
}

func (a *EchoAgent) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

func (a *EchoAgent) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, nil
}

// Prompt 一轮演示：先流式思考/正文，再发起真实权限确认；按用户决策回显结果与模拟工具卡。
func (a *EchoAgent) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	sid := params.SessionId
	text := promptText(params.Prompt)
	toolID := acpsdk.ToolCallId(fmt.Sprintf("tc-echo-%d", a.n.Add(1)))

	push := func(updates ...acpsdk.SessionUpdate) {
		if a.conn == nil {
			return
		}
		for _, u := range updates {
			_ = a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: sid, Update: u})
		}
	}

	push(
		acpsdk.UpdateAgentThoughtText("（echo agent · 本地演示：我将先发起一次工具权限确认，模拟真实 agent 在工具调用前的授权步骤，随后按你的决策继续。）"),
		acpsdk.UpdateAgentMessageText(fmt.Sprintf("收到你的输入：%s\n\n为完成它，我需要运行演示命令 `echo hello`（纯本地模拟，不真实执行）。", text)),
	)

	resp, err := a.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
		SessionId: sid,
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: toolID,
			Title:      acpsdk.Ptr("运行演示命令 echo hello"),
			Kind:       acpsdk.Ptr(acpsdk.ToolKindExecute),
			RawInput:   map[string]any{"command": "echo hello"},
		},
		Options: permOptions,
	})
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}

	// 解析决策：Selected（allow/reject 选项被选中）或 Cancelled（超时/会话结束被宿主取消）。
	label, allowed, ok := "已取消（Cancelled）", false, false
	if sel := resp.Outcome.Selected; sel != nil {
		ok = true
		for _, o := range permOptions {
			if o.OptionId == sel.OptionId {
				label = o.Name
				break
			}
		}
		allowed = strings.HasPrefix(string(sel.OptionId), "allow")
	}

	switch {
	case !ok:
		push(acpsdk.UpdateAgentMessageText("未获授权（决策被拒绝或取消，按 ACP 语义即 Cancelled）。跳过该演示命令。"))
	case allowed:
		push(
			acpsdk.StartToolCall(toolID, "运行演示命令 echo hello",
				acpsdk.WithStartKind(acpsdk.ToolKindExecute),
				acpsdk.WithStartStatus(acpsdk.ToolCallStatusInProgress),
				acpsdk.WithStartRawInput(map[string]any{"command": "echo hello"})),
			acpsdk.UpdateToolCall(toolID,
				acpsdk.WithUpdateKind(acpsdk.ToolKindExecute),
				acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusCompleted),
				acpsdk.WithUpdateRawOutput("hello")),
			acpsdk.UpdateAgentMessageText(fmt.Sprintf("已授权（%s）。模拟执行完成：`echo hello` → hello。", label)),
		)
	default:
		push(acpsdk.UpdateAgentMessageText(fmt.Sprintf("已拒绝（%s）。跳过该演示命令。", label)))
	}

	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

// promptText 取首个文本段（演示用，足够）。
func promptText(blocks []acpsdk.ContentBlock) string {
	for _, b := range blocks {
		if b.Text != nil && b.Text.Text != "" {
			return b.Text.Text
		}
	}
	return "（空输入）"
}
