package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/runtime/echo"
)

// acpServe 把内置 echo ACP 底座暴露成远程端点（A5）：
// 本机/跨机的另一个 ark-forge 可以在 .forge/config/runtimes.json 里把它声明为远程 runtime，
// 从而在不装 opencode/dsh、不用模型 key 的前提下演示/验证远程 ACP 链路。
func acpServe(args []string) {
	fs := flag.NewFlagSet("acp-serve", flag.ExitOnError)
	transport := fs.String("transport", "ws", "远程传输形态：ws（WebSocket）| http（Streamable HTTP）")
	listen := fs.String("listen", "127.0.0.1:10020", "监听地址（默认仅回环）")
	path := fs.String("path", "/acp", "端点路径")
	_ = fs.Parse(args)

	opts := acl.RemoteServerOptions{
		Serve:           func(in io.Writer, out io.Reader) { echo.Serve(in, out) },
		SessionIDPrefix: "echo",
		Logger:          slog.Default(),
	}
	mux := http.NewServeMux()
	switch strings.ToLower(strings.TrimSpace(*transport)) {
	case "ws", "websocket":
		mux.HandleFunc(*path, func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			_ = acl.ServeWSSession(ctx, conn, opts)
		})
	case "http", "streamable_http", "streamable-http":
		mux.Handle(*path, acl.NewRemoteHandler(opts))
	default:
		fmt.Fprintf(os.Stderr, "acp-serve: 不支持的 transport %q（ws|http）\n", *transport)
		os.Exit(2)
	}

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("acp-serve: echo ACP 底座已暴露为远程端点", "transport", *transport, "listen", *listen, "path", *path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "acp-serve: %v\n", err)
		os.Exit(1)
	}
}
