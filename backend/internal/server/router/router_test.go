package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/internal/server/router"
)

// newEngine 走真实装配路径：manager.Service + router.New（含 /v1/forge 分组与默认中间件）。
func newEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	reg := runtime.DefaultRegistry()
	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	h := handler.New(svc, bus)
	return router.New(h)
}

type respEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func get(t *testing.T, e *gin.Engine, path string) *respEnvelope {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var env respEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("path=%s 响应非 JSON: %v body=%s", path, err, w.Body.String())
	}
	return &env
}

// TestRouterMountHealth /v1/forge/health 可达。
func TestRouterMountHealth(t *testing.T) {
	e := newEngine()
	req := httptest.NewRequest("GET", "/v1/forge/health", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Code != 0 || out.Msg != "ok" {
		t.Fatalf("health 不符: %+v body=%s", out, w.Body.String())
	}
}

// TestRouterListRealService 经真实 Service 的列表端点：内置 agent dsh/opencode 两个（dsh 默认）、
// runtimes 3 个（opencode / deepseekharness / echo 演示）。
func TestRouterListRealService(t *testing.T) {
	e := newEngine()

	env := get(t, e, "/v1/forge/agents")
	if env.Code != 0 {
		t.Fatalf("agents 信封不符: %+v", env)
	}
	var agents []map[string]any
	if err := json.Unmarshal(env.Data, &agents); err != nil || len(agents) != 2 || agents[0]["id"] != "dsh" {
		t.Fatalf("agents data 不符: %s", env.Data)
	}
	if agents[0]["is_default"] != true || agents[1]["id"] != "opencode" || agents[1]["is_default"] != false {
		t.Fatalf("agents 默认标记不符: %s", env.Data)
	}

	env = get(t, e, "/v1/forge/runtimes")
	var rts []map[string]any
	if err := json.Unmarshal(env.Data, &rts); err != nil || len(rts) != 3 {
		t.Fatalf("runtimes data 不符: %s", env.Data)
	}

	env = get(t, e, "/v1/forge/conversations")
	if env.Code != 0 {
		t.Fatalf("conversations 信封不符: %+v", env)
	}
}

// TestRouterCapabilitiesListRealService P1 能力数据面经真实 Service：未装配目录时
// /skills /mcps /tools 返回空数组（REST 可列出能力的最小形态）。
func TestRouterCapabilitiesListRealService(t *testing.T) {
	e := newEngine()
	for _, p := range []string{"/skills", "/mcps", "/tools"} {
		env := get(t, e, "/v1/forge"+p)
		if env.Code != 0 {
			t.Fatalf("%s 信封不符: %+v", p, env)
		}
		var items []map[string]any
		if err := json.Unmarshal(env.Data, &items); err != nil || len(items) != 0 {
			t.Fatalf("%s 应为空数组: %s", p, env.Data)
		}
	}
}

// TestRouterRoutesRegistered 关键路由（REST + WS）均已注册于 /v1/forge 前缀。
func TestRouterRoutesRegistered(t *testing.T) {
	e := newEngine()
	routes := e.Routes()
	found := map[string]bool{}
	for _, rt := range routes {
		key := rt.Method + " " + rt.Path
		found[key] = true
	}
	for _, want := range []string{
		"GET /v1/forge/health",
		"GET /v1/forge/agents",
		"GET /v1/forge/runtimes",
		"GET /v1/forge/conversations",
		"POST /v1/forge/conversations",
		"GET /v1/forge/conversations/:id/history",
		"POST /v1/forge/conversations/:id/prompt",
		"POST /v1/forge/conversations/:id/cancel",
		"GET /v1/forge/providers",
		"GET /v1/forge/skills",
		"GET /v1/forge/mcps",
		"GET /v1/forge/tools",
		"GET /v1/forge/ws/events",
	} {
		if !found[want] {
			t.Fatalf("路由未注册: %s", want)
		}
	}
}

// TestRouterUnknownAPINotSPA 未注册的 /v1/* 返回 JSON 404（非 SPA index）。
func TestRouterUnknownAPINotSPA(t *testing.T) {
	e := newEngine()
	req := httptest.NewRequest("GET", "/v1/forge/nope", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("未知 /v1 路径应 404 JSON: code=%d body=%s", w.Code, w.Body.String())
	}
}

// TestReadmeRoutesMatchRouter 文档一致性守卫：README「控制面接口摘要」里出现的每条
// `/v1/forge` 路由（表格与正文反引号内）都必须真实注册；反之，README 声明的路径不得是历史名
// （P0 把 /sessions 改名为 /conversations 后曾长期漂移）。新增/改名路由若忘改文档即红。
func TestReadmeRoutesMatchRouter(t *testing.T) {
	engine := newEngine()
	registered := map[string]string{} // "METHOD /path" → 路径
	for _, r := range engine.Routes() {
		if !strings.HasPrefix(r.Path, "/v1/forge") {
			continue
		}
		registered[r.Method+" "+r.Path] = r.Path
	}
	if len(registered) == 0 {
		t.Fatal("未取到任何 /v1/forge 路由（装配异常）")
	}

	readme, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("读 README: %v", err)
	}
	// 抓取反引号内的路径片段：`/xxx` 或以 ` ` 分隔的多路径（如 `/skills` · `/mcps`）。
	re := regexp.MustCompile("`(/[A-Za-z0-9_\\-/:.]*)`")
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(readme), -1) {
		p := m[1]
		if !strings.HasPrefix(p, "/agents") && !strings.HasPrefix(p, "/conversations") &&
			!strings.HasPrefix(p, "/runtimes") && !strings.HasPrefix(p, "/providers") &&
			!strings.HasPrefix(p, "/skills") && !strings.HasPrefix(p, "/mcps") &&
			!strings.HasPrefix(p, "/tools") && !strings.HasPrefix(p, "/usage") &&
			!strings.HasPrefix(p, "/health") && !strings.HasPrefix(p, "/ws/") {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if _, ok := registered["GET /v1/forge"+p]; ok {
			continue
		}
		// 非 GET 的路由按方法前缀查（POST/PATCH/DELETE）。
		found := false
		for method := range registered {
			if strings.HasSuffix(method, " "+"/v1/forge"+p) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("README 引用了未注册路由 %s（已注册：%d 条）", p, len(registered))
		}
	}
	if len(seen) < 20 {
		t.Fatalf("README 路由提取过少（%d），守卫失效", len(seen))
	}
	// 反向：/sessions 历史名不得再出现在 README 的路由语境里。
	if strings.Contains(string(readme), "/sessions/") || strings.Contains(string(readme), "`/sessions`") {
		t.Error("README 仍含历史路由 /sessions（应为 /conversations）")
	}
	t.Logf("README 路由 %d 条全部命中注册表（注册 %d 条）", len(seen), len(registered))
}
