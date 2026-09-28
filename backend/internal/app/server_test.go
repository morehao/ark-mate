package app

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestHTTPServerRandomPort 监听地址为 `:0` 时**实际**端口由内核分配（M1-2 桌面态的起点）：
// Addr() 必须回报分配后的真实地址，且能被真的访问到。
func TestHTTPServerRandomPort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	srv, err := newHTTPServer("127.0.0.1:0", eng)
	if err != nil {
		t.Fatalf("newHTTPServer: %v", err)
	}
	addr := srv.Addr()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("Addr() 不是 host:port：%q（%v）", addr, err)
	}
	if host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("期望内核分配的真实端口，got %q", addr)
	}
	if port == "10001" {
		t.Fatalf("随机端口撞上了默认端口 10001（理论上可能但概率极低，重跑即可；本条只是提示）")
	}

	go func() { _ = srv.Start() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	// 用实际地址访问，确认真的在监听（而不只是返回了一个字符串）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/ping")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP 状态不符: %d", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("实际监听地址不可达: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHTTPServerBindConflictIsInitError 端口被占是**装配期**错误（退出码语义更准），
// 而不是运行期某个 goroutine 里的日志。
func TestHTTPServerBindConflictIsInitError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer ln.Close()

	if _, err := newHTTPServer(ln.Addr().String(), gin.New()); err == nil {
		t.Fatal("端口被占时应返回错误")
	}
}
