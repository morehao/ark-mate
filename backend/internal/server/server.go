// Package server 是控制面服务模块：基于 Gin 组织 REST API 与实时事件通道（WS，M0 落地）。
// 结构：router 负责 gin.Engine 初始化与路由注册，handler 按功能实现业务处理。
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/internal/server/router"
)

// Server 控制面 HTTP 服务，封装 gin.Engine 的启停生命周期。
type Server struct {
	httpSrv *http.Server
}

// Config 控制面服务配置。
type Config struct {
	Addr string // 监听地址，如 ":8099"
}

// New 构造控制面 Server。svc 为领域服务实现（manager 落地前可用 handler.NewMockService）。
// bus 承载归一化会话事件（架构文档 §2 范式），由实时通道（WS，M0 落地）订阅推送。
func New(cfg Config, svc handler.Service, bus *eventbus.Bus) *Server {
	h := handler.New(svc, bus)
	engine := router.New(h)

	engine.ForwardedByClientIP = true
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	httpSrv.BaseContext = func(net.Listener) context.Context {
		return context.Background()
	}

	return &Server{httpSrv: httpSrv}
}

// Engine 返回底层 gin.Engine（供测试/扩展挂载路由）。
func (s *Server) Engine() *gin.Engine {
	return s.httpSrv.Handler.(*gin.Engine)
}

// Start 启动 HTTP 服务并阻塞至退出。
func (s *Server) Start() error {
	fmt.Printf("[server] listening on %s\n", s.httpSrv.Addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 平滑关闭 HTTP 服务。
func (s *Server) Stop(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
