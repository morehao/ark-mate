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
type httpServer struct {
	srv *http.Server
}

// newHTTPServer 用已装配好的引擎构造 HTTP 服务。
//
// 默认值取向：只给读头超时（慢客户端攻击面），不设读写整体超时——
// WS 是长连接，写超时会掐断实时事件流；TLS 最低 1.2。
func newHTTPServer(addr string, engine *gin.Engine) *httpServer {
	engine.ForwardedByClientIP = true
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	srv.BaseContext = func(net.Listener) context.Context {
		return context.Background()
	}
	return &httpServer{srv: srv}
}

// Engine 返回底层 gin.Engine（供测试/扩展挂载路由）。
func (s *httpServer) Engine() *gin.Engine {
	return s.srv.Handler.(*gin.Engine)
}

// Start 启动 HTTP 服务并阻塞至退出。
func (s *httpServer) Start() error {
	fmt.Printf("[server] listening on %s\n", s.srv.Addr)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 平滑关闭 HTTP 服务。
func (s *httpServer) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
