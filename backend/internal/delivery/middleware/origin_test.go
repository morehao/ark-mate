package middleware_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrws"
	"github.com/morehao/ark-mate/internal/delivery/middleware"
)

// 本文件是 M3-5（Origin 矩阵 + Host 白名单）的判据。三块：
//  1. 判定表（Host / Origin 各自的正反例）；
//  2. **REST 中间件与 WS 握手判定逐条一致**——同一组用例跑两遍，这是"两处不分叉"的机器证据；
//  3. 真 WS 握手（coder/websocket）在错误的 Origin 下被拒、在矩阵内的 Origin 下能建立。

// originCase 一组判定用例。denied 是**期望**（放行/拒绝），两侧实现都必须给出同一结论。
type originCase struct {
	name   string
	host   string
	origin string
	denied bool
	// status 期望的拒绝状态码（denied 时必填）：421 = 走错主机，403 = 来源不在矩阵内。
	status int
}

// policyFor 构造"核心监听在 listenAddr、额外允许表为 extra"的策略。
func policyFor(t *testing.T, listenAddr string, extra ...string) *middleware.OriginPolicy {
	t.Helper()
	p := middleware.NewOriginPolicy(listenAddr, extra)
	if !p.Enabled() {
		t.Fatalf("listenAddr=%q 应启用策略（本用例假定回环监听）", listenAddr)
	}
	return p
}

// envelope 是统一响应信封里本文件关心的两个字段。
type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// serveThroughREST 走真实中间件挂载（与 router.New 里 v1 组同款：先 Use 再注册路由）。
func serveThroughREST(t *testing.T, p *middleware.OriginPolicy, method, path, host, origin string) (int, envelope) {
	t.Helper()
	eng := gin.New()
	eng.Use(middleware.Origin(p))
	eng.GET(path, func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"}) })
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env
}

// serveThroughWSHandler 走真实的 WS 控制器（只挂它，不挂组中间件）——测的就是 WS 那一侧的判定。
//
// 用普通 GET（不带 upgrade 头）即可：控制器的判定发生在 websocket.Accept **之前**，
// 被拒时回的是我们自己的 421/403 信封；放行时才会走到 Accept 并因"不是升级请求"失败。
func serveThroughWSHandler(t *testing.T, p *middleware.OriginPolicy, path, host, origin string) (int, envelope) {
	t.Helper()
	eng := gin.New()
	h := ctrws.NewWSCtr(eventbus.New(), ctrws.Options{OriginCheck: p.CheckFunc()})
	eng.GET(path, h.Handle)
	req := httptest.NewRequest("GET", path, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env
}

// TestOriginPolicyHostWhitelist Host 白名单：主机 ∈ {127.0.0.1,[::1],localhost} 且端口 == 实际监听端口。
func TestOriginPolicyHostWhitelist(t *testing.T) {
	p := policyFor(t, "127.0.0.1:10001")
	cases := []originCase{
		{name: "127.0.0.1+端口", host: "127.0.0.1:10001"},
		{name: "IPv6 回环", host: "[::1]:10001"},
		{name: "localhost", host: "localhost:10001"},
		{name: "大小写不敏感", host: "LocalHost:10001"},
		{name: "域名", host: "evil.com:10001", denied: true, status: 421},
		{name: "端口不符", host: "127.0.0.1:9999", denied: true, status: 421},
		{name: "缺端口", host: "localhost", denied: true, status: 421},
		{name: "前缀伪装", host: "127.0.0.1.evil.com:10001", denied: true, status: 421},
		{name: "后缀伪装", host: "localhost.evil.com:10001", denied: true, status: 421},
		{name: "端口里夹域名", host: "127.0.0.1:10001.evil.com", denied: true, status: 421},
		{name: "空 Host", host: "", denied: true, status: 421},
		{name: "其它回环地址", host: "127.0.0.2:10001", denied: true, status: 421},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := serveThroughREST(t, p, "GET", "/v1/mate/health", tc.host, "")
			if !tc.denied {
				if status != http.StatusOK {
					t.Fatalf("Host=%q 应放行，got %d", tc.host, status)
				}
				return
			}
			if status != tc.status {
				t.Fatalf("Host=%q 应以 %d 拒绝，got %d", tc.host, tc.status, status)
			}
		})
	}
}

// TestOriginPolicyOriginMatrix Origin 矩阵：无 Origin / mate-app://app / 自身监听端口的同源 / 额外允许表。
func TestOriginPolicyOriginMatrix(t *testing.T) {
	p := policyFor(t, "127.0.0.1:51234", "http://localhost:5173", "https://mate.example.com")
	host := "127.0.0.1:51234"
	cases := []originCase{
		{name: "无 Origin（壳内反代/curl/导航）", host: host},
		{name: "桌面直连 mate-app://app", host: host, origin: "mate-app://app"},
		{name: "同源 127.0.0.1", host: host, origin: "http://127.0.0.1:51234"},
		{name: "同源 localhost", host: host, origin: "http://localhost:51234"},
		{name: "同源 IPv6", host: host, origin: "http://[::1]:51234"},
		{name: "同源大小写变体", host: host, origin: "HTTP://LocalHost:51234"},
		{name: "额外表命中", host: host, origin: "http://localhost:5173"},
		{name: "额外表大小写+尾斜杠归一", host: host, origin: "HTTP://LOCALHOST:5173/"},
		{name: "额外表 https 反代", host: host, origin: "https://mate.example.com"},
		{name: "Origin: null（沙箱 iframe）", host: host, origin: "null", denied: true, status: 403},
		{name: "https 外域", host: host, origin: "https://evil.com", denied: true, status: 403},
		{name: "本地其它端口", host: host, origin: "http://127.0.0.1:9999", denied: true, status: 403},
		{name: "localhost 其它端口", host: host, origin: "http://localhost:9999", denied: true, status: 403},
		{name: "默认端口 10001 不是本进程端口", host: host, origin: "http://localhost:10001", denied: true, status: 403},
		{name: "另一套自定义 scheme", host: host, origin: "mate-app://other", denied: true, status: 403},
		{name: "同源但 https（回环只有明文）", host: host, origin: "https://127.0.0.1:51234", denied: true, status: 403},
		{name: "同源但缺端口", host: host, origin: "http://127.0.0.1", denied: true, status: 403},
		{name: "端口后缀伪装", host: host, origin: "http://127.0.0.1:51234.evil.com", denied: true, status: 403},
		{name: "额外表前缀伪装", host: host, origin: "http://localhost:5173.evil.com", denied: true, status: 403},
		{name: "userinfo 夹带", host: host, origin: "http://127.0.0.1:51234@evil.com", denied: true, status: 403},
		{name: "Host 与 Origin 都错（Host 先判）", host: "evil.com:51234", origin: "http://evil.com:51234", denied: true, status: 421},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, env := serveThroughREST(t, p, "GET", "/v1/mate/health", tc.host, tc.origin)
			if !tc.denied {
				if status != http.StatusOK {
					t.Fatalf("Host=%q Origin=%q 应放行，got %d", tc.host, tc.origin, status)
				}
				return
			}
			if status != tc.status {
				t.Fatalf("Host=%q Origin=%q 应以 %d 拒绝，got %d", tc.host, tc.origin, tc.status, status)
			}
			// 信封必须带业务码：不带码的错误会让客户端落到 golib 的 code:-1 兜底，无从判定。
			if env.Code != code.InvalidRequest {
				t.Fatalf("拒绝信封应带码 %d，got %+v", code.InvalidRequest, env)
			}
			if strings.TrimSpace(env.Msg) == "" {
				t.Fatalf("拒绝信封应带可读原因，got %+v", env)
			}
		})
	}
}

// TestOriginPolicyDisabledOnNonLoopback 非回环监听不启用白名单（设计 §9.4）：判定一律放行。
//
// 这条同时钉住"为什么"：启用条件是**监听主机 ∈ Host 白名单集合**，而不是"看起来像本机"。
// 所以 0.0.0.0 / :10001 / 127.0.0.2 都不启用——白名单表达不了这些主机，硬启用只会把
// 每个请求都打成 421（服务不可用），那不是更安全。
func TestOriginPolicyDisabledOnNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:10001", ":10001", "[::]:10001", "127.0.0.2:10001", "10.0.0.9:10001"} {
		p := middleware.NewOriginPolicy(addr, nil)
		if p.Enabled() {
			t.Fatalf("addr=%q 不应启用白名单", addr)
		}
		req := httptest.NewRequest("GET", "/v1/mate/health", nil)
		req.Host = "evil.com:10001"
		req.Header.Set("Origin", "http://evil.com")
		if d := p.Decide(req); !d.Allowed() {
			t.Fatalf("addr=%q 未启用时不应拒绝任何请求，got %+v", addr, d)
		}
		if p.CheckFunc() != nil {
			t.Fatalf("addr=%q 未启用时 WS 判定回调应为 nil（控制器完全跳过判定）", addr)
		}
	}

	// nil 策略同样放行（Options.Origin 缺省 = 不判定）。
	var nilPolicy *middleware.OriginPolicy
	req := httptest.NewRequest("GET", "/v1/mate/health", nil)
	req.Host = "evil.com"
	if d := nilPolicy.Decide(req); !d.Allowed() {
		t.Fatalf("nil 策略应放行，got %+v", d)
	}
	if h := middleware.Origin(nilPolicy); h == nil {
		t.Fatal("nil 策略应返回 no-op 处理器")
	}
}

// consistencyCases 是 REST / WS 两侧**共用**的判定用例（含 Host 反例：WS 握手也可能带错的 Host）。
func consistencyCases() []originCase {
	return []originCase{
		{name: "合法 Host 无 Origin", host: "127.0.0.1:40001"},
		{name: "合法 Host 桌面 Origin", host: "127.0.0.1:40001", origin: "mate-app://app"},
		{name: "合法 Host 同源 Origin", host: "127.0.0.1:40001", origin: "http://localhost:40001"},
		{name: "合法 Host 额外表命中", host: "localhost:40001", origin: "http://localhost:5173"},
		{name: "非法 Host", host: "evil.com:40001", denied: true, status: 421},
		{name: "宿主端口不符", host: "127.0.0.1:40002", denied: true, status: 421},
		{name: "缺端口 Host", host: "localhost", denied: true, status: 421},
		{name: "Origin null", host: "127.0.0.1:40001", origin: "null", denied: true, status: 403},
		{name: "Origin 外域", host: "127.0.0.1:40001", origin: "https://evil.com", denied: true, status: 403},
		{name: "Origin 本地其它端口", host: "127.0.0.1:40001", origin: "http://127.0.0.1:9999", denied: true, status: 403},
		{name: "Host 与 Origin 同错", host: "evil.com:40001", origin: "http://evil.com:40001", denied: true, status: 421},
	}
}

// TestOriginPolicyRESTAndWSAgree REST 中间件与 WS 握手判定**逐条一致**（M3-5 的核心要求：
// 两处各写一份判定迟早分叉，WS 侧漏掉 Host 判定就等于"换个协议绕过白名单"）。
//
// 断言三件事：放行/拒绝结论相同、拒绝状态码相同、拒绝信封 `code` 与 `msg` 逐字相同
// （msg 相同的唯一可能是两侧调用了**同一个** Decide——文案里带着实际端口与来源）。
func TestOriginPolicyRESTAndWSAgree(t *testing.T) {
	p := policyFor(t, "127.0.0.1:40001", "http://localhost:5173")
	for _, tc := range consistencyCases() {
		t.Run(tc.name, func(t *testing.T) {
			restStatus, restEnv := serveThroughREST(t, p, "GET", "/v1/mate/health", tc.host, tc.origin)
			wsStatus, wsEnv := serveThroughWSHandler(t, p, "/v1/mate/ws/events", tc.host, tc.origin)

			restDenied := restStatus == http.StatusForbidden || restStatus == http.StatusMisdirectedRequest
			wsDenied := wsStatus == http.StatusForbidden || wsStatus == http.StatusMisdirectedRequest
			if restDenied != tc.denied || wsDenied != tc.denied {
				t.Fatalf("期望 denied=%v：REST status=%d，WS status=%d", tc.denied, restStatus, wsStatus)
			}
			if restDenied != wsDenied {
				t.Fatalf("REST 与 WS 结论不一致：REST=%d WS=%d（Host=%q Origin=%q）", restStatus, wsStatus, tc.host, tc.origin)
			}
			if !tc.denied {
				return
			}
			if restStatus != wsStatus {
				t.Fatalf("拒绝状态码不一致：REST=%d WS=%d", restStatus, wsStatus)
			}
			if restStatus != tc.status {
				t.Fatalf("拒绝状态码不符：want %d got %d", tc.status, restStatus)
			}
			if restEnv != wsEnv {
				t.Fatalf("拒绝信封不一致（说明两侧不是同一个判定函数）：REST=%+v WS=%+v", restEnv, wsEnv)
			}
			if restEnv.Code != code.InvalidRequest {
				t.Fatalf("拒绝信封应带码 %d，got %+v", code.InvalidRequest, restEnv)
			}
		})
	}
}

// TestWSHandshakeOriginRejectedOnRealListener 真握手（coder/websocket）：
// 同一份策略下，矩阵内的来源建得起连接，矩阵外的来源被拒且回的是我们的 403 信封。
//
// 这里用真实 TCP 监听器（端口来自 net.Listen），与生产装配同一条路径：策略里的端口
// 必须是**实际监听端口**，否则"同源"这条规则无从判定。
func TestWSHandshakeOriginRejectedOnRealListener(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	addr := ln.Addr().String()
	p := policyFor(t, addr, "http://localhost:5173")

	eng := gin.New()
	h := ctrws.NewWSCtr(eventbus.New(), ctrws.Options{OriginCheck: p.CheckFunc()})
	eng.GET("/v1/mate/ws/events", h.Handle)

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

	// 放行：无 Origin（壳内/脚本）、桌面直连、自身端口同源、额外表命中。
	for _, origin := range []string{"", "mate-app://app", "http://" + addr, "http://localhost:5173"} {
		conn, resp, err := dial(origin)
		if err != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			t.Fatalf("Origin=%q 应建立握手，got err=%v status=%d", origin, err, status)
		}
		conn.CloseNow()
	}

	// 拒绝：来源不在矩阵内。状态码与信封由我们给（不是库的兜底），所以能断言 403 + 业务码。
	for _, origin := range []string{"null", "https://evil.com", "http://127.0.0.1:9999"} {
		conn, resp, err := dial(origin)
		if err == nil {
			conn.CloseNow()
			t.Fatalf("Origin=%q 应被拒绝，但握手成功了", origin)
		}
		if resp == nil {
			t.Fatalf("Origin=%q 拒绝时应回 HTTP 响应，got err=%v", origin, err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("Origin=%q 应以 403 拒绝，got %d", origin, resp.StatusCode)
		}
		var env envelope
		if derr := json.NewDecoder(resp.Body).Decode(&env); derr != nil {
			t.Fatalf("Origin=%q 拒绝响应非 JSON 信封: %v", origin, derr)
		}
		resp.Body.Close()
		if env.Code != code.InvalidRequest {
			t.Fatalf("Origin=%q 拒绝信封应带码 %d，got %+v", origin, code.InvalidRequest, env)
		}
	}
}
