package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// stopGrace HTTP 优雅停机预算（信号到达后留给在途请求的时间）。
const stopGrace = 5 * time.Second

// httpServer 控制面 HTTP 服务，封装 `http.Handler` 的启停生命周期。
//
// 与路由分离的原因：路由（internal/delivery/router）回答"哪些路径由谁处理"，
// 这里回答"监听在哪、超时多久、怎么优雅退出"——两者的变更理由完全不同。
//
// **监听器在构造期就建好**（而不是 ListenAndServe 内部再建）：桌面态的地址是
// `127.0.0.1:0`（内核分配端口），核心必须能读出**实际**端口才能回写就绪行（M1-2）；
// 同时把"端口被占"从运行期错误提前成装配期错误（退出码语义更准）。
type httpServer struct {
	ln  net.Listener
	srv *http.Server
}

// listen 绑定监听地址。
//
// 独立成函数（M3-5）：Host 白名单要把 Host 的端口与**实际监听端口**比对，而桌面态的端口是
// `127.0.0.1:0` 下内核分配的随机值——只有绑定之后才知道。所以装机顺序是
// 「解析资产 → 绑定 → 用 ln.Addr() 构造来源策略 → 装配路由 → 起服务」，
// 端口既不是猜的（配置里写的是 0），也不是跳过的（策略拿不到端口就等于没有同源判据）。
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	return ln, nil
}

// newHTTPServer 绑定监听地址并构造 HTTP 服务（参数是配置里的地址）。
func newHTTPServer(addr string, h http.Handler) (*httpServer, error) {
	ln, err := listen(addr)
	if err != nil {
		return nil, err
	}
	return newHTTPServerOn(ln, h), nil
}

// newHTTPServerOn 用**已绑定**的监听器构造 HTTP 服务（地址与端口已定，不可再改）。
//
// 默认值取向：只给读头超时（慢客户端攻击面），不设读写整体超时——
// WS 是长连接，写超时会掐断实时事件流；TLS 最低 1.2。
func newHTTPServerOn(ln net.Listener, h http.Handler) *httpServer {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	srv.BaseContext = func(net.Listener) context.Context {
		return context.Background()
	}
	return &httpServer{ln: ln, srv: srv}
}

// Handler 返回底层 HTTP handler（供测试挂载/断言路由）。
//
// 这里**不做类型断言**：应用壳不需要知道 Handler 背后是 gin 还是别的框架。
func (s *httpServer) Handler() http.Handler { return s.srv.Handler }

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
