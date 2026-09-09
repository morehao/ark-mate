package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime/echo"
)

// newEchoRemote 起一个真实 ACP server（内置 echo 底座）挂在远程端点上：
// ws 形态走 acl.ServeWSSession，http 形态走 acl.NewRemoteHandler（均为生产服务端半场）。
func newEchoRemote(t *testing.T, transport string) string {
	t.Helper()
	opts := acl.RemoteServerOptions{Serve: func(in io.Writer, out io.Reader) { echo.Serve(in, out) }, SessionIDPrefix: "echo"}
	mux := http.NewServeMux()
	switch transport {
	case "ws":
		mux.HandleFunc("/acp", func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			_ = acl.ServeWSSession(ctx, conn, opts)
		})
	case "http":
		mux.Handle("/acp", acl.NewRemoteHandler(opts))
	default:
		t.Fatalf("未知传输 %q", transport)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if transport == "ws" {
		return "ws" + strings.TrimPrefix(srv.URL, "http") + "/acp"
	}
	return srv.URL + "/acp"
}

// TestConnectRemoteTransport runtime.Connect 按 LaunchSpec.Transport 走远程传输（A5）：
// 真实 echo ACP server 在远端，Connect 完成 initialize 并跑通一轮会话（含权限交互）。
func TestConnectRemoteTransport(t *testing.T) {
	for _, transport := range []string{"ws", "http"} {
		t.Run(transport, func(t *testing.T) {
			endpoint := newEchoRemote(t, transport)
			spec := RemoteSpec("remote-echo", "Remote Echo", transport, endpoint, nil)
			if spec.Launch.Transport != transport || spec.Launch.Endpoint != endpoint {
				t.Fatalf("RemoteSpec 未落到 Launch: %+v", spec.Launch)
			}
			events := make(chan acl.Event, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cl, err := Connect(ctx, spec, nil, nil, acl.ClientOptions{
				OnEvent: func(e acl.Event) { events <- e },
				RequestPermission: func(_ context.Context, _ acl.PermissionRequest) acl.PermissionDecision {
					return acl.PermissionDecision{Allowed: true, OptionID: "allow-all", Always: true}
				},
			})
			if err != nil {
				t.Fatalf("Connect(%s): %v", transport, err)
			}
			defer cl.Close()
			if cl.PidInfo().PID != 0 {
				t.Fatalf("远程 runtime 不应报告 pid: %+v", cl.PidInfo())
			}
			sess, err := cl.NewSession(ctx, acl.SessionOptions{Cwd: "/tmp"})
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
			// echo 底座每轮必发一次权限请求 + 回显消息。
			var gotPermission, gotMessage bool
			deadline := time.After(5 * time.Second)
		loop:
			for {
				select {
				case e := <-events:
					switch e.Kind {
					case acl.KindPermission:
						gotPermission = true
					case acl.KindMessage:
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
	_, err := Connect(context.Background(), spec, nil, nil, acl.ClientOptions{})
	if err == nil {
		t.Fatal("应报错")
	}
	if cls := acl.Classify(err).Class; cls != acl.ClassUnsupported {
		t.Fatalf("应归类 unsupported，得到 %v (%v)", cls, err)
	}
}
