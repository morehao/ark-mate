package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// stopGrace HTTP 优雅停机预算（信号到达后留给在途请求的时间）。
const stopGrace = 5 * time.Second

// httpServer 控制面 HTTP 服务，封装 gin.Engine 的启停生命周期。
//
// 与路由分离的原因：路由（internal/router）回答"哪些路径由谁处理"，
// 这里回答"监听在哪、超时多久、怎么优雅退出"——两者的变更理由完全不同。
//
// **监听器在构造期就建好**（而不是 ListenAndServe 内部再建）：桌面态的地址是
// `127.0.0.1:0`（内核分配端口），核心必须能读出**实际**端口才能回写就绪行（M1-2）；
// 同时把"端口被占"从运行期错误提前成装配期错误（退出码语义更准）。
type httpServer struct {
	ln  net.Listener
	srv *http.Server
}

// newHTTPServer 用已装配好的引擎构造 HTTP 服务，并立即绑定监听地址。
//
// 默认值取向：只给读头超时（慢客户端攻击面），不设读写整体超时——
// WS 是长连接，写超时会掐断实时事件流；TLS 最低 1.2。
func newHTTPServer(addr string, engine *gin.Engine) (*httpServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	engine.ForwardedByClientIP = true
	srv := &http.Server{
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	srv.BaseContext = func(net.Listener) context.Context {
		return context.Background()
	}
	return &httpServer{ln: ln, srv: srv}, nil
}

// Engine 返回底层 gin.Engine（供测试/扩展挂载路由）。
func (s *httpServer) Engine() *gin.Engine {
	return s.srv.Handler.(*gin.Engine)
}

// Addr 返回**实际**监听地址（配置为 `:0` 时是内核分配后的真实端口）。
func (s *httpServer) Addr() string { return s.ln.Addr().String() }

// Start 在已绑定的监听器上启动 HTTP 服务并阻塞至退出。
func (s *httpServer) Start() error {
	fmt.Printf("[server] listening on %s\n", s.Addr())
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 平滑关闭 HTTP 服务。
func (s *httpServer) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
