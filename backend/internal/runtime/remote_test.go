package runtime

import (
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// newTestRemote 起一个真实 ACP server（自建最小 responder，见 acp_testserver_test.go）挂在
// 远程端点上：ws 形态走 acp.ServeWSSession，http 形态走 acp.NewRemoteHandler（均为生产服务端半场）。
func newTestRemote(t *testing.T, transport string) string {
	t.Helper()
	r := newTestACPResponder()
	r.updates = []acpsdk.SessionUpdate{acpsdk.UpdateAgentMessageText("remote-hello")}
	r.promptHook = func(ctx context.Context, r *testACPResponder, req acpsdk.PromptRequest) {
		r.permResult <- r.requestPermission(ctx, req.SessionId)
	}
	switch transport {
	case "ws":
		return serveResponderWS(t, r)
	case "http":
		return serveResponderHTTP(t, r)
	default:
		t.Fatalf("未知传输 %q", transport)
		return ""
	}
}

// TestConnectRemoteTransport runtime.Connect 按 LaunchSpec.Transport 走远程传输（A5）：
// 真实 ACP server 在远端，Connect 完成 initialize 并跑通一轮会话（含权限交互）。
// 对端由自建最小 responder 提供（echo 已删）；ws/http 两形态都覆盖，保住远程传输路径。
func TestConnectRemoteTransport(t *testing.T) {
	for _, transport := range []string{"ws", "http"} {
		t.Run(transport, func(t *testing.T) {
			endpoint := newTestRemote(t, transport)
			spec := RemoteSpec("remote-rt", "Remote RT", transport, endpoint, nil)
			if spec.Launch.Transport != transport || spec.Launch.Endpoint != endpoint {
				t.Fatalf("RemoteSpec 未落到 Launch: %+v", spec.Launch)
			}
			events := make(chan acp.Event, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cl, err := Connect(ctx, spec, nil, nil, acp.ClientOptions{
				OnEvent: func(e acp.Event) { events <- e },
				RequestPermission: func(_ context.Context, _ acp.PermissionRequest) acp.PermissionDecision {
					return acp.PermissionDecision{Allowed: true, OptionID: "allow-all", Always: true}
				},
			})
			if err != nil {
				t.Fatalf("Connect(%s): %v", transport, err)
			}
			defer cl.Close()
			if cl.PidInfo().PID != 0 {
				t.Fatalf("远程 runtime 不应报告 pid: %+v", cl.PidInfo())
			}
			sess, err := cl.NewSession(ctx, acp.SessionOptions{Cwd: "/tmp"})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			stop, err := sess.Prompt(ctx, "远程 runtime 冒烟")
			if err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			if stop != "end_turn" {
				t.Fatalf("stop reason 不符: %q", stop)
			}
			// responder 每轮必发一次权限请求 + 一条消息更新。
			var gotPermission, gotMessage bool
			deadline := time.After(5 * time.Second)
		loop:
			for {
				select {
				case e := <-events:
					switch e.Kind {
					case acp.KindPermission:
						gotPermission = true
					case acp.KindMessage:
						gotMessage = true
					}
					if gotPermission && gotMessage {
						break loop
					}
				case <-deadline:
					break loop
				}
			}
			if !gotPermission || !gotMessage {
				t.Fatalf("远程链路事件不全（permission=%v message=%v）", gotPermission, gotMessage)
			}
		})
	}
}

// TestConnectUnsupportedTransport 非法传输在 Connect 阶段即报 unsupported（不静默回落 stdio）。
func TestConnectUnsupportedTransport(t *testing.T) {
	spec := Spec{ID: "bad", Launch: LaunchSpec{Transport: "grpc", Endpoint: "127.0.0.1:1"}}
	_, err := Connect(context.Background(), spec, nil, nil, acp.ClientOptions{})
	if err == nil {
		t.Fatal("应报错")
	}
	// 出口错误是**平台视图**（Connect 在把错误交给上层前折叠），分类按平台词汇判。
	re, ok := code.RuntimeErrorOf(err)
	if !ok || re.Class != code.ClassUnsupported {
		t.Fatalf("应归类 unsupported，得到 %+v (%v)", re, err)
	}
}
