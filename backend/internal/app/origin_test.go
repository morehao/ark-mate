package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/config"
)

// 本文件是 M3-5 的**进程内集成**判据：走真实 Init（真起服务、真建 engine、真监听端口），
// 断言"实际监听端口从哪来"这件事真的接上了——策略里的端口必须等于 ln.Addr() 给出的那个，
// 而不是配置里的 `:0` 或默认 10001。

// originTestApp 装配一个可用的 App（内存持久层 + 假资产目录 + 随机端口），并返回它的
// **实际**地址与 engine。资产用假目录：这样 SPA 回落路由也真实存在（挂载范围用例需要它）。
func originTestApp(t *testing.T, mutate func(cfg *config.Config)) (*App, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}

	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.TranscriptDir = filepath.Join(home, "transcripts")
	cfg.DBPath = filepath.Join(home, "forge.db")
	cfg.StoreMode = config.StoreModeMemory
	cfg.WorkspaceDir = ""     // 关闭代建（本测试不建会话）
	cfg.ProcRegistryPath = "" // 不写进程注册表
	cfg.AutomationTick = 0    // 关闭调度
	cfg.IdleTimeout = 0       // 关闭空闲回收
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0" // 桌面态：内核分配端口（策略里的端口只能是实际值）
	cfg.WebDir = web
	cfg.ProjectionDirs = map[string]string{
		"opencode:skill":        filepath.Join(home, "proj-opencode"),
		"deepseekharness:skill": filepath.Join(home, "proj-dsh"),
	}
	if mutate != nil {
		mutate(&cfg)
	}

	a := New(cfg, nil)
	if err := a.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if a.srv != nil {
			_ = a.srv.Stop(ctx)
		}
		a.Close()
	})
	return a, a.srv.Addr()
}

// doRequest 打一次真实 engine（Host / Origin 可控）。
func doRequest(t *testing.T, a *App, method, path, host, origin string) (int, envelopeOf) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	a.Engine().ServeHTTP(w, req)
	var env envelopeOf
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env
}

// envelopeOf 统一信封里本文件关心的两个字段。
type envelopeOf struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// TestInitOriginPolicyUsesRealListenPort 装配路径真的把**实际监听端口**交给了策略。
func TestInitOriginPolicyUsesRealListenPort(t *testing.T) {
	a, addr := originTestApp(t, nil)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("实际地址不是 host:port: %q", addr)
	}
	if port == "0" || port == "10001" {
		t.Fatalf("本用例要的是内核分配的真实端口，got %q", addr)
	}

	// 合法 Host + 无 Origin → 放行。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, ""); status != http.StatusOK {
		t.Fatalf("合法 Host 无 Origin 应 200，got %d", status)
	}
	// 合法 Host + **实际端口**的同源 Origin → 放行（这条正是"端口不是猜的"的证据：
	// 配置里写的是 `:0`，默认值是 10001，只有 ln.Addr() 才知道真实端口）。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://"+addr); status != http.StatusOK {
		t.Fatalf("同源 Origin 应 200，got %d", status)
	}
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://localhost:"+port); status != http.StatusOK {
		t.Fatalf("localhost 同源 Origin 应 200，got %d", status)
	}
	// 反向确认：默认端口（配置默认值 10001）**不是**本进程的端口 → 403。
	if status, env := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://localhost:10001"); status != http.StatusForbidden ||
		env.Code != code.InvalidRequest {
		t.Fatalf("默认端口 10001 的 Origin 应 403+带码，got %d %+v", status, env)
	}
	// 本地其它端口 → 403。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://127.0.0.1:9999"); status != http.StatusForbidden {
		t.Fatalf("本地其它端口 Origin 应 403，got %d", status)
	}
	// 错误 Host → 421。
	status, env := doRequest(t, a, "GET", "/v1/forge/health", "evil.com:"+port, "")
	if status != http.StatusMisdirectedRequest || env.Code != code.InvalidRequest {
		t.Fatalf("错误 Host 应 421+带码，got %d %+v", status, env)
	}
	// 错误 Host 即使 Origin 也对 → 仍 421（Host 先判）。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", "evil.com:"+port, "http://"+addr); status != http.StatusMisdirectedRequest {
		t.Fatalf("错误 Host 应 421（与 Origin 无关），got %d", status)
	}
}

// TestInitOriginPolicyExtraAllowTable 配置的额外允许表真的生效（`make dev` 的 vite :5173 就靠它）。
func TestInitOriginPolicyExtraAllowTable(t *testing.T) {
	a, addr := originTestApp(t, func(cfg *config.Config) {
		cfg.AllowedOrigins = []string{"http://localhost:5173"}
	})
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://localhost:5173"); status != http.StatusOK {
		t.Fatalf("额外表命中的 Origin 应 200，got %d", status)
	}
	// 大小写 + 尾斜杠变体同样命中（与 pkg/config 的归一同一规则）。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "HTTP://LocalHost:5173/"); status != http.StatusOK {
		t.Fatalf("额外表归一后的变体应 200，got %d", status)
	}
	// 没登记的邻近端口仍被拒（登记口不是"放开一整个网段"）。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", addr, "http://localhost:5174"); status != http.StatusForbidden {
		t.Fatalf("未登记的邻近端口应 403，got %d", status)
	}
}

// TestInitOriginPolicyDoesNotGateStaticAssets 挂载范围：白名单只覆盖 `/v1/forge` 组，
// 静态资产与 SPA 回落不受影响（否则"打开面板"本身就是 421）。
func TestInitOriginPolicyDoesNotGateStaticAssets(t *testing.T) {
	a, addr := originTestApp(t, nil)
	// 页面导航（无 Origin）+ 任意 Host：仍应拿到 index.html（DNS rebinding 拿不到 API，
	// 但页面导航不该被白名单打死——这是挂载范围的取舍，见 router.New 的注释）。
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "evil.com:" + strings.TrimPrefix(addr, "127.0.0.1:")
	w := httptest.NewRecorder()
	a.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("静态入口不应被来源白名单拦截：got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Fatalf("静态入口应回 index.html，got %s", w.Body.String())
	}
	// 同一 Host 打 API 仍被拒——证明上一条不是因为"策略整体没生效"。
	if status, _ := doRequest(t, a, "GET", "/v1/forge/health", "evil.com:1234", ""); status != http.StatusMisdirectedRequest {
		t.Fatalf("同 Host 打 API 应 421，got %d", status)
	}
}

// TestOriginPolicyDisabledWhenNonLoopback 非回环监听（`--addr 0.0.0.0`）**不启用**白名单：
// 设计 §9.4 把 Host/Origin 白名单列为对外产品化缺的四件事之一，本期范围外（启动打 WARN）。
//
// 不必真绑 0.0.0.0（那会把端口暴露到本机网络）：直接按监听地址构造策略，再放进真实路由装配里
// 断言"任意 Host/Origin 都放行"——这正是未启用时的行为。
func TestOriginPolicyDisabledWhenNonLoopback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := middleware.NewOriginPolicy("0.0.0.0:10001", []string{"http://localhost:5173"})
	if p.Enabled() {
		t.Fatal("非回环监听不应启用白名单")
	}
	eng := router.New(router.Ports{}, eventbus.New(), router.Options{Origin: p})

	req := httptest.NewRequest("GET", "/v1/forge/health", nil)
	req.Host = "evil.com:10001"
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("非回环监听下不应有来源判定：got %d body=%s", w.Code, w.Body.String())
	}
}
