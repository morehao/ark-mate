package ctrws_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/control/sessions"
	"github.com/morehao/ark-mate/internal/controller/ctrws"
	"github.com/morehao/ark-mate/internal/core/eventbus"
)

// newRevokeEngine 装配一个带「会话校验 + 集中会话登记」的 WS 引擎。
//
// verify 从 query 的 access_token 解析 sid：空 → 401；其余原样当 sid。用于验证「SLO 按 sid
// 撤销连接」的接线，不牵扯真实 JWT 验签（那部分由 control/slo 的端到端测试覆盖）。
func newRevokeEngine(bus *eventbus.Bus, reg *sessions.Registry) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	h := ctrws.NewWSCtr(bus, ctrws.Options{
		VerifySession: func(r *http.Request) (string, int, error) {
			tok := r.URL.Query().Get("access_token")
			if tok == "" {
				return "", http.StatusUnauthorized, errors.New("缺少 access_token")
			}
			return tok, 0, nil
		},
		Sessions: reg,
	})
	eng.GET("/ws/events", h.Handle)
	return eng
}

// dialWSWithToken 拨号并带上 access_token（=sid）。
func dialWSWithToken(t *testing.T, eng *gin.Engine, token string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	if token != "" {
		wsURL += "?access_token=" + token
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// TestWSRevokeBySIDClosesConnection 按 sid 撤销 → 该 sid 的连接被关闭；他 sid 的连接不受影响。
func TestWSRevokeBySIDClosesConnection(t *testing.T) {
	bus := eventbus.New()
	reg := sessions.NewRegistry()
	eng := newRevokeEngine(bus, reg)

	c1 := dialWSWithToken(t, eng, "sid-A")
	c2 := dialWSWithToken(t, eng, "sid-B")

	// 握手：各发一条通道级 attach，确认连接可用。
	wsWrite(t, c1, map[string]any{"action": "attach"})
	wsWrite(t, c2, map[string]any{"action": "attach"})

	// 等到两条连接都完成登记（避免竞态：握手异步）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reg.ActiveCount("sid-A") == 1 && reg.ActiveCount("sid-B") == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reg.ActiveCount("sid-A") != 1 || reg.ActiveCount("sid-B") != 1 {
		t.Fatalf("应各登记 1 条：A=%d B=%d", reg.ActiveCount("sid-A"), reg.ActiveCount("sid-B"))
	}

	// 撤销 sid-A → c1 应被关闭（Read 返回错误）。
	n, err := reg.RevokeBySID(context.Background(), "sid-A")
	if err != nil || n != 1 {
		t.Fatalf("撤销 sid-A 应关闭 1 条：n=%d err=%v", n, err)
	}

	// c1 的读取应失败（连接已被服务端关闭）。
	readDone := make(chan error, 1)
	go func() {
		_, _, rerr := c1.Read(context.Background())
		readDone <- rerr
	}()
	select {
	case rerr := <-readDone:
		if rerr == nil {
			t.Fatal("sid-A 连接应已被服务端关闭，但 Read 成功")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sid-A 连接未在超时内关闭")
	}

	// c2 仍可用：ping → pong。
	wsWrite(t, c2, map[string]any{"action": "ping"})
	var pong frame
	wsRead(t, c2, &pong)
	if pong.Type != "pong" {
		t.Fatalf("sid-B 连接仍应可用（期望 pong），got %+v", pong)
	}
}

// TestWSRejectsWithoutToken 无 access_token → 握手被拒（401），不建立连接。
func TestWSRejectsWithoutToken(t *testing.T) {
	bus := eventbus.New()
	reg := sessions.NewRegistry()
	eng := newRevokeEngine(bus, reg)

	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("无令牌应握手失败")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("期望 401，got status=%d err=%v", code, err)
	}
}
