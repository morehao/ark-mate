package router_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/pkg/code"
)

// 本文件是 M3-5 在**路由装配层**的判据：白名单挂在哪一组、WS 握手是不是同一条判定。

// originEngine 用真实 router.New 装配一个带来源策略的引擎（端口用给定的实际监听地址）。
func originEngine(t *testing.T, policy *middleware.OriginPolicy) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}
	assets, err := router.ResolveAssets(web)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	return router.New(testRouterDeps(t, testRouterOpts{origin: policy, web: assets}))
}

// TestRouterOriginPolicyMountScope 白名单只覆盖 `/v1/mate` 组：API 被判定，静态入口不判定。
func TestRouterOriginPolicyMountScope(t *testing.T) {
	policy := middleware.NewOriginPolicy("127.0.0.1:10001", nil)
	if !policy.Enabled() {
		t.Fatal("本用例假定回环监听")
	}
	eng := originEngine(t, policy)

	call := func(path, host, origin string) (int, map[string]any) {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	// API：错误 Host → 421（带码）、错误 Origin → 403（带码）、正确 Host+Origin → 200。
	if status, body := call("/v1/mate/health", "evil.com:10001", ""); status != http.StatusMisdirectedRequest ||
		int(body["code"].(float64)) != code.InvalidRequest {
		t.Fatalf("API 错误 Host 应 421+带码，got %d %v", status, body)
	}
	if status, body := call("/v1/mate/health", "127.0.0.1:10001", "http://127.0.0.1:9999"); status != http.StatusForbidden ||
		int(body["code"].(float64)) != code.InvalidRequest {
		t.Fatalf("API 错误 Origin 应 403+带码，got %d %v", status, body)
	}
	if status, _ := call("/v1/mate/health", "127.0.0.1:10001", "http://127.0.0.1:10001"); status != http.StatusOK {
		t.Fatalf("API 正确 Host+Origin 应 200，got %d", status)
	}

	// 静态入口（页面导航无 Origin）：不受来源判定影响，否则"打开面板"本身就打不开。
	status, body := call("/", "evil.com:10001", "")
	if status != http.StatusOK {
		t.Fatalf("静态入口不应被来源白名单拦截：got %d %v", status, body)
	}
	if ct := body["code"]; ct != nil {
		t.Fatalf("静态入口不该回 API 信封：%v", body)
	}
}

// TestRouterWSHandshakeHonorsOriginPolicy WS 握手走**同一个**策略（M3-5）：
// 矩阵内的来源能建立连接，矩阵外的来源拿不到 upgrade（回我们自己的 403 信封）。
//
// 这条**只**经由 WS 控制器生效（事件流的组不带 Origin 中间件，见 router.New 的分线说明），
// 所以它同时是"registerStream 真的把策略交给了控制器"的证据：漏传策略时，矩阵外的来源
// 会在这里拿到 101 而不是 403。
//
// 用真实监听器（端口来自 net.Listen）而不是 httptest：策略里的端口必须是实际监听端口，
// 这是本任务"端口不能猜"的核心断言。
func TestRouterWSHandshakeHonorsOriginPolicy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	addr := ln.Addr().String()
	policy := middleware.NewOriginPolicy(addr, nil)
	eng := originEngine(t, policy)

	srv := &http.Server{Handler: eng}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	url := "ws://" + addr + "/v1/mate/ws/events"
	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		opts := &websocket.DialOptions{}
		if origin != "" {
			opts.HTTPHeader = http.Header{"Origin": []string{origin}}
		}
		return websocket.Dial(ctx, url, opts)
	}

	// 桌面直连的 Origin 是矩阵里唯一不可由"自身监听端口"表达的那条，必须能建起来。
	conn, resp, err := dial(middleware.DesktopAppOrigin)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("%s 应能建立 WS 握手，got err=%v status=%d", middleware.DesktopAppOrigin, err, status)
	}
	conn.CloseNow()

	// 矩阵外的来源（本地其它端口）必须被拒——且拒的是我们的信封（带业务码）。
	conn, resp, err = dial("http://127.0.0.1:9999")
	if err == nil {
		conn.CloseNow()
		t.Fatal("矩阵外的 Origin 应被拒绝，但握手成功了")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("应以 403 拒绝，got %+v", resp)
	}
	var env struct {
		Code int `json:"code"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&env); derr != nil {
		t.Fatalf("拒绝响应非 JSON 信封: %v", derr)
	}
	resp.Body.Close()
	if env.Code != code.InvalidRequest {
		t.Fatalf("拒绝信封应带码 %d，got %+v", code.InvalidRequest, env)
	}
}

// TestRouterNoOriginPolicyIsNoop 未传策略（Options.Origin 为 nil）= 不判定，既有行为不变。
//
// 这条防的是"把策略接成必填"：既有测试与只跑 API 的进程都不该被连带改变。
func TestRouterNoOriginPolicyIsNoop(t *testing.T) {
	eng := originEngine(t, nil)
	req := httptest.NewRequest("GET", "/v1/mate/health", nil)
	req.Host = "evil.com"
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("无策略时应放行：got %d %s", w.Code, w.Body.String())
	}
}
