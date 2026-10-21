package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dsh"

	"github.com/morehao/ark-mate/internal/domain/workspace"
	credmech "github.com/morehao/ark-mate/internal/drivers/mechanism/credential"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/platform/credential"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/delivery/router"
	"github.com/morehao/ark-mate/internal/contract/dtoworkspace"
	"github.com/morehao/ark-mate/internal/runtime"
	apprun "github.com/morehao/ark-mate/internal/app/run"
)

// newEngine 走真实装配路径：apprun.Service + router.New（含 /v1/mate 分组与默认中间件）。
func newEngine(t *testing.T) *gin.Engine {
	t.Helper()
	e, _ := newEngineWithService(t)
	return e
}

// newEngineWithService 真实装配并把 Service 交出来：需要先设策略（如工作区根白名单）的用例用它。
//
// 资产按**生产同一条路径**解析（M2-1）：release 构建内嵌 → 有资产（非 API 未知路径回落 SPA）；
// 默认构建 → 没有（回落信封 404）。忽略解析错误是刻意的——"没有资产"对只测 API 的用例是合法状态，
// 而生产入口（serve）走的是 app.Init，那里会按退出码 3 拒绝启动。
func newEngineWithService(t *testing.T) (*gin.Engine, *apprun.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	reg := dsh.Registry()
	// 生产装配**总是**在构造 Service 之前注入隔离 env（`app.go`）；测试也照做，
	// 否则模型配置落点会因为解析不到隔离根而报错（这是刻意的 fail-closed：绝不回落到用户目录）。
	reg.ApplyIsolationEnv(filepath.Join(os.TempDir(), "mate-router-test-home"))
	bus := eventbus.New()
	svc := newSessionService(reg, bus)
	// v20：这条夹具是 **memory 模式**（没有 sqlite 层），所以密钥的落点是托管文档
	// `<MATE_HOME>/credentials.json`——`POST/PATCH providers` 带 `api_key` 时它就是事实源，
	// 文档没装配会被**显式拒绝**（`credentials_write_failed`：拒绝一次只在内存里成功的写入）。
	// home 与 providerDir 都在 `t.TempDir()` 下，绝不碰用户环境（`~/.ark-mate`）。
	home := t.TempDir()
	creds, _, err := credmech.OpenCredentialSource(home, home)
	if err != nil {
		t.Fatalf("装配凭据分层: %v", err)
	}
	// 继承环境那一层置空：这条夹具不该被跑测试的机器上恰好存在的同名变量影响
	// （层序本身由 `pkg/credentials` 的用例与 manager 的用例覆盖）。
	creds.Env = func(string) string { return "" }
	planesOf(svc).Credentials.SetLayered(creds)
	planesOf(svc).Providers.SetProviderDir(filepath.Join(home, "providers"))
	assets, _ := router.ResolveAssets("")
	engine := router.New(testRouterDeps(t, testRouterOpts{web: assets, service: svc, registry: reg, bus: bus}))
	return engine, svc
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

// TestRouterMountHealth /v1/mate/health 可达。
func TestRouterMountHealth(t *testing.T) {
	e := newEngine(t)
	req := httptest.NewRequest("GET", "/v1/mate/health", nil)
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

// TestRouterRuntimeIdentityRealService 经真实 Service 的**运行时身份**读面。
//
// v19：这里原本逐条核对"每个内置底座各播一个种子 agent、其中构建期选中的那个是默认"。agent 面
// 撤销后，同一件事只剩两个事实出口：`GET /meta` 的 `runtime`（构建期固定的 id/name/version）
// 与 `GET /runtime/diagnostics`（此刻的进程态）。两者都不带可选语义——是事实，不是选项。
func TestRouterRuntimeIdentityRealService(t *testing.T) {
	e := newEngine(t)

	env := get(t, e, "/v1/mate/meta")
	if env.Code != 0 {
		t.Fatalf("meta 信封不符: %+v", env)
	}
	var meta struct {
		Runtime struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(env.Data, &meta); err != nil {
		t.Fatalf("meta data 反序列化失败: %s", env.Data)
	}
	if meta.Runtime.ID != runtime.SelectedRuntime() || meta.Runtime.Name == "" {
		t.Fatalf("meta.runtime 应如实报构建期选中的底座（%s）: %s", runtime.SelectedRuntime(), env.Data)
	}

	env = get(t, e, "/v1/mate/runtime/diagnostics")
	if env.Code != 0 {
		t.Fatalf("runtime/diagnostics 信封不符: %+v", env)
	}
	if !strings.Contains(string(env.Data), `"runtime":"`+runtime.SelectedRuntime()+`"`) {
		t.Fatalf("诊断面应带底座 id: %s", env.Data)
	}
	// 身份面不许回带 agent 选择面（被撤掉的概念不许从别的字段缝回来）。
	for _, banned := range []string{`"agent_id"`, `"runtimes"`} {
		if strings.Contains(string(env.Data), banned) {
			t.Fatalf("诊断面不该出现 %s: %s", banned, env.Data)
		}
	}

	env = get(t, e, "/v1/mate/sessions")
	if env.Code != 0 {
		t.Fatalf("sessions 信封不符: %+v", env)
	}
}

// TestRouterRoutesRegistered 关键路由（REST + WS）均已注册于 /v1/mate 前缀。
func TestRouterRoutesRegistered(t *testing.T) {
	e := newEngine(t)
	routes := e.Routes()
	found := map[string]bool{}
	for _, rt := range routes {
		key := rt.Method + " " + rt.Path
		found[key] = true
	}
	for _, want := range []string{
		"GET /v1/mate/health",
		"GET /v1/mate/runtime/diagnostics",
		"GET /v1/mate/sessions",
		"POST /v1/mate/sessions",
		"GET /v1/mate/sessions/:id/history",
		"POST /v1/mate/sessions/:id/prompt",
		"POST /v1/mate/sessions/:id/cancel",
		"GET /v1/mate/providers",
		"GET /v1/mate/models",
		"GET /v1/mate/skills",
		"POST /v1/mate/skills/import",
		"GET /v1/mate/mcps",
		"GET /v1/mate/tools",
		"GET /v1/mate/ws/events",
	} {
		if !found[want] {
			t.Fatalf("路由未注册: %s", want)
		}
	}
	// **负向断言**：`/runtimes` 必须不存在（用户面零 runtime 选择入口；见 pkg/runtime/build.go）。
	if found["GET /v1/mate/runtimes"] {
		t.Fatal("GET /v1/mate/runtimes 不该再注册：底座由构建期固定，用户面没有这个读面")
	}
}

// TestRouterUnknownAPINotSPA 未命中的 **API** 路径一律回标准信封 404。
//
// 这条测试此前是假的：断言只有 `w.Code == 404 && strings.Contains(body, "not found")`，
// 而非 release 构建根本没注册 NoRoute，gin 默认的纯文本 "404 page not found" 恰好也含
// "not found"——测试在开发态是**靠碰巧**通过的，从来没验证过信封。现在改为真正解析 JSON，
// 逐项断言信封四要素与 code 取值。
//
// 只覆盖 API 路径：非 API 路径的期望值因构建形态而异（dev 回信封 404、release 回落 SPA
// index.html），拆在 notfound_dev_test.go 与 notfound_release_test.go 里各自断言。
func TestRouterUnknownAPINotSPA(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"未注册的 API 路径", "/v1/mate/nope"},
		{"未注册的 API 深层路径", "/v1/mate/sessions/x/nope"},
	}
	e := newEngine(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, req)

			if w.Code != 404 {
				t.Fatalf("应回 HTTP 404，得到 %d（body=%s）", w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("应是 JSON 而非纯文本/SPA，Content-Type=%q body=%s", ct, w.Body.String())
			}
			var env struct {
				Code      int            `json:"code"`
				RequestID string         `json:"requestID"`
				Msg       string         `json:"msg"`
				Data      map[string]any `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("响应体不是信封 JSON: %v（body=%s）", err, w.Body.String())
			}
			// code 必须取自 pkg/code 注册表：此前手写的 404 不在码表里，客户端无法判定。
			if env.Code != code.NotFound {
				t.Errorf("信封 code 应为 code.NotFound(%d)，得到 %d", code.NotFound, env.Code)
			}
			if env.Msg == "" {
				t.Error("信封 msg 不应为空（客户端按 msg 展示）")
			}
			if env.Data == nil {
				t.Error("信封缺 data 字段（形状应与 gincontext.Fail 一致）")
			}
			// requestID 允许为空（AccessLog 挂分组、NoRoute 挂引擎，分组中间件不执行），
			// 但字段本身必须在——前端 Envelope<T> 按它取。
			if !strings.Contains(w.Body.String(), `"requestID"`) {
				t.Error("信封缺 requestID 字段")
			}
		})
	}
}

// 说明：新增路由**族**时要把前缀加进下面这串过滤条件，否则守卫对它形同虚设；但前缀不能太宽——
// README 正文里的前端页面路由（如 `/settings`）会被一并抓成"未注册路由"，
// 故控制面设置面这条用的是 `/settings/` 而不是 `/settings`。
//
// repoFile 定位仓库根下的文件：从当前测试文件所在目录逐级上溯，返回首个命中的相对路径。
//
// 不用固定层数的 "../../.."：包一旦挪位置（如 router 从 internal/server/router 上提），
// 这类相对路径就会静默失准，表现为"文档守卫读不到 README"而不是断言失败。
func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, self, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位仓库根")
	}
	dir := filepath.Dir(self)
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, name)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("自 %s 上溯未找到 %s", filepath.Dir(self), name)
	return ""
}

// TestReadmeRoutesMatchRouter 文档一致性守卫：README「控制面接口摘要」与真实路由表**双向**一致。
//
//   - README → router：README 里出现的每条 `/v1/mate` 路由（表格与正文反引号内）都必须真实注册。
//     抓得住改名/删除（P0 把 /sessions 改名为 /sessions 后曾长期漂移），也连带锁住 README
//     不再引用历史名。
//   - router → README：每条注册的 `/v1/mate` 路由都必须在 README 出现。**这一向曾经缺失**，
//     后果是新增端点可以不带文档静默进主干（只有改名才红）。
//
// 两个方向都指向同一条约定：README 的接口摘要是路由契约，改路由与改文档必须同一次提交。
func TestReadmeRoutesMatchRouter(t *testing.T) {
	engine := newEngine(t)
	registered := map[string]string{} // "METHOD /path" → 路径
	for _, r := range engine.Routes() {
		if !strings.HasPrefix(r.Path, "/v1/mate") {
			continue
		}
		registered[r.Method+" "+r.Path] = r.Path
	}
	if len(registered) == 0 {
		t.Fatal("未取到任何 /v1/mate 路由（装配异常）")
	}

	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatalf("读 README: %v", err)
	}
	// 抓取反引号内的路径片段：`/xxx` 或以 ` ` 分隔的多路径（如 `/skills` · `/mcps`）。
	re := regexp.MustCompile("`(/[A-Za-z0-9_\\-/:.]*)`")
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(readme), -1) {
		p := m[1]
		if !strings.HasPrefix(p, "/runtime") && !strings.HasPrefix(p, "/sessions") &&
			!strings.HasPrefix(p, "/runtimes") && !strings.HasPrefix(p, "/providers") &&
			!strings.HasPrefix(p, "/models") && !strings.HasPrefix(p, "/workspaces") &&
			!strings.HasPrefix(p, "/skills") && !strings.HasPrefix(p, "/mcps") &&
			!strings.HasPrefix(p, "/tools") && !strings.HasPrefix(p, "/library") &&
			!strings.HasPrefix(p, "/automations") &&
			!strings.HasPrefix(p, "/usage") &&
			!strings.HasPrefix(p, "/search") && !strings.HasPrefix(p, "/settings/") && !strings.HasPrefix(p, "/command-policy") &&
			!strings.HasPrefix(p, "/health") &&
			!strings.HasPrefix(p, "/meta") &&
			!strings.HasPrefix(p, "/capabilities") &&
			!strings.HasPrefix(p, "/ws/") {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if _, ok := registered["GET /v1/mate"+p]; ok {
			continue
		}
		// 非 GET 的路由按方法前缀查（POST/PATCH/DELETE）。
		found := false
		for method := range registered {
			if strings.HasSuffix(method, " "+"/v1/mate"+p) {
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
	// 反向：`/agents*` 是 **v19 之前的历史名**（agent 概念整个并入 runtime），不得再出现在
	// README 的路由语境里——与 `conversation` 同形：历史名留在文档里会让下一个人以为它还在册。
	if strings.Contains(string(readme), "`/agents") {
		t.Error("README 仍含历史路由 /agents*（v19 起 agent 面并入 runtime）")
	}
	// 反向：`conversation` 是 **v15 之前的历史名**，不得再出现在 README 的路由语境里。
	//
	// 方向在 2026-10-11 的 v15 改名里翻转：此前正名是 /conversations、历史名是 /sessions，
	// 现在正好相反。留成"两边都不许出现"会让改名永远无法落地。
	if strings.Contains(string(readme), "/conversations/") || strings.Contains(string(readme), "`/conversations`") {
		t.Error("README 仍含历史路由 /conversations（v15 起正名为 /sessions）")
	}
	// 反向（router → README）：每条注册的 /v1/mate 路由都必须在 README 出现。
	//
	// 上面那条只保证 README 不撒谎，抓得到改名/删除（README 引用的路径没注册）；但抓不到
	// **新增**——端点悄悄上线、README 只字未提，守卫照样绿。补上这一向才是双向契约。
	// 兼容别名同样算端点（历史上 `/permissions/always` 两个别名正是靠这条才被写进文档）。
	var undocumented []string
	for key, path := range registered {
		if !seen[strings.TrimPrefix(path, "/v1/mate")] {
			undocumented = append(undocumented, key)
		}
	}
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("注册了未写进 README 的路由 %d 条——README「控制面接口摘要」是本项目的路由契约，新增端点必须同表补齐：\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "))
	}
	t.Logf("README 路由 %d 条全部命中注册表（注册 %d 条）", len(seen), len(registered))
}

// newAuthEngine 装配带凭据表的引擎（M1-1）。
func newAuthEngine(t *testing.T, creds *credential.Set) *gin.Engine {
	gin.SetMode(gin.TestMode)
	reg := dsh.Registry()
	// 生产装配**总是**在构造 Service 之前注入隔离 env（`app.go`）；测试也照做，
	// 否则模型配置落点会因为解析不到隔离根而报错（这是刻意的 fail-closed：绝不回落到用户目录）。
	reg.ApplyIsolationEnv(filepath.Join(os.TempDir(), "mate-router-test-home"))
	bus := eventbus.New()
	svc := newSessionService(reg, bus)
	engine := router.New(testRouterDeps(t, testRouterOpts{creds: creds, service: svc, registry: reg, bus: bus}))
	return engine
}

// standaloneCreds 独立态凭据：单一令牌同时具备 rest 与 stream 两个面
// （= MATE_TOKEN 折叠成的形态，见 internal/bootstrap/app 的 credentials()）。
func standaloneCreds(token string) *credential.Set {
	if token == "" {
		return nil
	}
	return credential.MustNew(credential.Pair{Token: token, Scopes: credential.ScopeREST | credential.ScopeStream})
}

// TestAuthToken 独立态单令牌：/health 免鉴权；其余 REST 路由要求 Bearer。
//
// **M1-1 的破坏性变更点**：`?token=` 已删除（令牌进 URL 会同时进浏览器历史、反代日志与
// Referer），因此"查询参数带正确令牌"从放行变成 401——这一行就是那条变更的回归点。
// 事件流的鉴权也搬到了带内（首帧 attach），所以 WS 路由本身不再由 REST 中间件把守。
func TestAuthToken(t *testing.T) {
	const token = "s3cret-token"
	eng := newAuthEngine(t, standaloneCreds(token))

	type probe struct {
		name       string
		method     string
		path       string
		authHeader string
		wantStatus int
		wantCode   int
	}
	cases := []probe{
		{"health 免鉴权", "GET", "/v1/mate/health", "", 200, 0},
		{"缺令牌", "GET", "/v1/mate/sessions", "", 401, 40101},
		{"错令牌", "GET", "/v1/mate/sessions", "Bearer wrong", 401, 40101},
		{"非 Bearer 方案", "GET", "/v1/mate/sessions", "Basic s3cret-token", 401, 40101},
		{"正确 Bearer", "GET", "/v1/mate/sessions", "Bearer " + token, 200, 0},
		{"方案大小写不敏感", "GET", "/v1/mate/sessions", "bearer " + token, 200, 0},
		{"查询参数令牌已失效（M1-1 删除 ?token=）", "GET", "/v1/mate/sessions?token=" + token, "", 401, 40101},
		{"POST 也受保护", "POST", "/v1/mate/sessions/export", "", 401, 40101},
		// WS 路由不挂 REST 凭据中间件（鉴权在带内首帧 attach）：非升级请求直接进入升级逻辑，
		// 被升级库拒绝为 426 Upgrade Required。关键是「不再是 401」。
		{"WS 不受 REST 中间件把守（鉴权在带内）", "GET", "/v1/mate/ws/events", "", 426, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			w := httptest.NewRecorder()
			eng.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("HTTP 状态不符: got %d want %d（body=%s）", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus == 401 {
				var env struct {
					Code      int    `json:"code"`
					RequestID string `json:"requestID"`
					Msg       string `json:"msg"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
					t.Fatalf("401 应返回 REST 信封: %v（body=%s）", err, w.Body.String())
				}
				if env.Code != tc.wantCode || env.RequestID == "" || env.Msg == "" {
					t.Fatalf("401 信封不符: %+v", env)
				}
			}
		})
	}
}

// TestCredentialsScopeSeparation 桌面态双凭据：两个面**互斥**（V15 的 REST 侧判据）。
//
// streamToken 打不到 REST 是这条分层的全部意义——事件流通道即使令牌被页面持有，
// 也拿不到任何写能力。反例（streamToken → REST 放行）会让 V15 失去意义，因此必须钉住。
func TestCredentialsScopeSeparation(t *testing.T) {
	const rest = "rest-token-2f9c"
	const stream = "stream-token-71ab"
	eng := newAuthEngine(t, credential.MustNew(
		credential.Pair{Token: rest, Scopes: credential.ScopeREST},
		credential.Pair{Token: stream, Scopes: credential.ScopeStream},
	))

	cases := []struct {
		name   string
		path   string
		header string
		want   int
	}{
		{"restToken → REST 放行", "/v1/mate/sessions", "Bearer " + rest, 200},
		{"streamToken → REST 被拒（V15）", "/v1/mate/sessions", "Bearer " + stream, 401},
		{"无令牌 → REST 被拒", "/v1/mate/sessions", "", 401},
		{"restToken 也不能走查询参数", "/v1/mate/sessions?token=" + rest, "", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			eng.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("HTTP 状态不符: got %d want %d（body=%s）", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestCredentialCoversAllRestRoutes 除 /health 与 /ws/events 外，**每一条**注册路由都必须
// 落在 rest 面凭据中间件之后。
//
// 为什么要有这条：中间件是挂在 `v1.Group("")` 上的，新写 routes_*.go 的人若把路由注册到
// `v1` 而不是 rest 组（或将来有人重构分组），那条端点会**静默变成免鉴权**——斜杠前面少一层
// 就差一个 401，普通用例根本发现不了。这里直接枚举真实注册表逐条打一遍。
func TestCredentialCoversAllRestRoutes(t *testing.T) {
	eng := newAuthEngine(t, credential.MustNew(
		credential.Pair{Token: "rest-token-2f9c", Scopes: credential.ScopeREST},
	))
	exempt := map[string]bool{
		"/v1/mate/health":    true, // liveness 探针免鉴权（容器编排不带令牌）
		"/v1/mate/ws/events": true, // 鉴权在带内首帧 attach
	}
	checked := 0
	for _, ri := range eng.Routes() {
		if !strings.HasPrefix(ri.Path, "/v1/mate") || exempt[ri.Path] {
			continue
		}
		checked++
		// 无 Authorization → 中间件必须先于 handler 拦下（不依赖任何领域端口就绪）。
		req := httptest.NewRequest(ri.Method, ri.Path, nil)
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Errorf("%s %s 未受 rest 面凭据保护（HTTP %d）——它注册到 rest 组之外了？",
				ri.Method, ri.Path, w.Code)
		}
	}
	if checked < 20 {
		t.Fatalf("只检查到 %d 条 REST 路由，明显少于实际注册数——过滤条件写错了，守卫形同虚设", checked)
	}
}

// TestAuthTokenDisabled 未配置凭据 = 历史行为（不鉴权）。
func TestAuthTokenDisabled(t *testing.T) {
	eng := newAuthEngine(t, nil)
	req := httptest.NewRequest("GET", "/v1/mate/sessions", nil)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("未配置凭据应放行: %d %s", w.Code, w.Body.String())
	}
}

// TestWorkspaceRoutes 走真实装配（apprun.Service）验证工作空间只读面：
// browse 只列目录且路径与 cwd 同形态；recent 从会话派生（新库为空）。
func TestWorkspaceRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "proj"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := newEngine(t)

	env := get(t, e, "/v1/mate/workspaces/browse?path="+url.QueryEscape(dir))
	if env.Code != 0 {
		t.Fatalf("browse 失败: %+v", env)
	}
	var browse dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &browse); err != nil {
		t.Fatalf("browse data 解析: %v", err)
	}
	if filepath.Base(browse.Path) != filepath.Base(dir) || len(browse.Entries) != 1 || browse.Entries[0].Name != "proj" {
		t.Fatalf("browse 应只列目录: %+v", browse)
	}
	if browse.Entries[0].Path != filepath.Join(browse.Path, "proj") {
		t.Fatalf("entry path 应为绝对路径: %+v", browse.Entries[0])
	}

	// 相对路径 → 信封非 0（工作空间校验与建会话同一套语义）。
	if env := get(t, e, "/v1/mate/workspaces/browse?path=relative"); env.Code == 0 {
		t.Fatalf("相对 path 应报错: %+v", env)
	}

	env = get(t, e, "/v1/mate/workspaces/recent")
	if env.Code != 0 {
		t.Fatalf("recent 失败: %+v", env)
	}
	var recent []dtoworkspace.WorkspaceRecentResp
	if err := json.Unmarshal(env.Data, &recent); err != nil {
		t.Fatalf("recent data 解析: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("新库无会话 → recent 应为空: %+v", recent)
	}

	if env := get(t, e, "/v1/mate/workspaces/recent?limit=abc"); env.Code != 40000 {
		t.Fatalf("非法 limit 应 40000: %+v", env)
	}
}

// TestWorkspaceRoutesWithRoots 工作区根白名单（MATE_WORKSPACE_ROOTS）在**真实装配**下的对外表现：
// 越界回语义错误码（业务码 40001，而不是笼统的 code=1）、上级止步于根、roots 随响应回读。
func TestWorkspaceRoutesWithRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	sub := filepath.Join(root, "proj")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{sub, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	e, svc := newEngineWithService(t)
	if err := planesOf(svc).Workspaces.SetWorkspacePolicy(filepath.Join(root, "tmp"), []string{root}); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}

	// 缺省起点 = 偏好名录的第一项；上级给到真实父目录（名录不再是边界）；roots 回读供面板列快捷入口。
	env := get(t, e, "/v1/mate/workspaces/browse")
	if env.Code != 0 {
		t.Fatalf("browse 失败: %+v", env)
	}
	var browse dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &browse); err != nil {
		t.Fatalf("browse data 解析: %v", err)
	}
	if len(browse.Roots) != 1 || filepath.Base(browse.Path) != "ws" {
		t.Fatalf("应从偏好起点起步并回读 roots: %+v", browse)
	}
	if browse.Parent != mustEvalPath(t, filepath.Dir(mustEvalPath(t, root))) {
		t.Fatalf("上级应为真实父目录（名录不再是边界）: %q", browse.Parent)
	}

	// 根内子目录：上级 = 根（允许回去）。
	env = get(t, e, "/v1/mate/workspaces/browse?path="+url.QueryEscape(sub))
	if env.Code != 0 {
		t.Fatalf("根内浏览失败: %+v", env)
	}
	var subResp dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &subResp); err != nil {
		t.Fatalf("sub data 解析: %v", err)
	}
	if filepath.Base(subResp.Parent) != "ws" {
		t.Fatalf("根内子目录的上级应为根: %+v", subResp)
	}

	// 名录外：**照常浏览**（v14.5 的产品口径：选工作区是全系统，名录只是浏览起点偏好）。
	env = get(t, e, "/v1/mate/workspaces/browse?path="+url.QueryEscape(outside))
	if env.Code != 0 {
		t.Fatalf("名录外目录应可浏览: %+v", env)
	}
}

// TestWorkspaceSettingsRoutes 设置面在**真实装配**下的完整闭环：
// PUT 落盘 config.json → 生效名单即时收窄（且代建根隐式并入）→ 重启后解析出同一份名单。
func TestWorkspaceSettingsRoutes(t *testing.T) {
	base := t.TempDir()
	cfgDir := t.TempDir()
	root := filepath.Join(base, "docs")
	provision := filepath.Join(base, "tmp")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// 预置其它键：写回是 read-modify-write，不得把运维的配置抹掉。
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"addr":"127.0.0.1:20001"}`), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	e, svc := newEngineWithService(t)
	if err := planesOf(svc).Workspaces.SetWorkspacePolicy(provision, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	planesOf(svc).Workspaces.SetRootsStore(config.NewSettingsStore(cfgDir, ""))

	// 起步：没有偏好起点（prefer_roots=false），可写，来源 = 默认值。
	env := get(t, e, "/v1/mate/settings/workspace")
	if env.Code != 0 {
		t.Fatalf("settings GET 失败: %+v", env)
	}
	var st dtoworkspace.WorkspaceSettingsResp
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("settings data 解析: %v", err)
	}
	if st.PreferRoots || !st.Writable || st.Source != "default" || len(st.Roots) != 0 {
		t.Fatalf("起步快照不符: %+v", st)
	}

	// 未画边界时任何可读目录都能浏览（默认的零配置体验不能被这次改造破坏）。
	if env = get(t, e, "/v1/mate/workspaces/browse?path="+url.QueryEscape(outside)); env.Code != 0 {
		t.Fatalf("不限制时根外目录应可浏览: %+v", env)
	}

	// PUT 画边界：生效名单 = 配置的根 + 隐式并入的代建根（顺序：配置项在前）。
	env = put(t, e, "/v1/mate/settings/workspace", `{"roots":[`+jsonString(root)+`]}`)
	if env.Code != 0 {
		t.Fatalf("settings PUT 失败: %+v", env)
	}
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("PUT data 解析: %v", err)
	}
	if !st.PreferRoots || !st.ProvisionIncluded || len(st.Configured) != 1 || len(st.Roots) != 2 {
		t.Fatalf("更新后快照不符: %+v", st)
	}
	if st.Roots[0] != mustEvalPath(t, root) || filepath.Base(st.Roots[1]) != "tmp" {
		t.Fatalf("生效名单应为「配置根 + 代建根」: %+v", st.Roots)
	}

	// 即时生效：偏好起点变了，但**不再是边界**——名录外照常可浏览。
	env = get(t, e, "/v1/mate/workspaces/browse?path="+url.QueryEscape(outside))
	if env.Code != 0 {
		t.Fatalf("设了偏好起点后名录外仍应可浏览: %+v", env)
	}
	defDir := filepath.Join(provision, "default")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	if _, err := workspace.ResolveWorkspace("", &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: defDir}); err != nil {
		t.Fatalf("默认工作空间应仍可用（代建根隐式并入）: %v", err)
	}

	// 落盘：非破坏性写（addr 保留）+ 数组形式（路径含逗号也能往返）。
	raw, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatalf("读 config.json: %v", err)
	}
	if !strings.Contains(string(raw), `"addr": "127.0.0.1:20001"`) {
		t.Fatalf("写回破坏了其它键: %s", raw)
	}
	cfg, _, err := config.LoadResolved(cfgDir)
	if err != nil {
		t.Fatalf("写回后配置应合法: %v", err)
	}
	if len(cfg.WorkspaceRoots) != 1 || cfg.WorkspaceRoots[0] != mustEvalPath(t, root) {
		t.Fatalf("重启后应解析出同一份名单: %+v", cfg.WorkspaceRoots)
	}

	// 坏项：逐项校验，回 40001 且不落盘。
	env = put(t, e, "/v1/mate/settings/workspace", `{"roots":["relative"]}`)
	if env.Code != 40001 {
		t.Fatalf("非法根应 40001: %+v", env)
	}

	// 被更高优先级来源锁定 → 拒绝写并自报只读（面板据此转只读，而不是假装保存成功）。
	planesOf(svc).Workspaces.SetRootsStore(config.NewSettingsStore(cfgDir, config.SourceEnv))
	env = put(t, e, "/v1/mate/settings/workspace", `{"roots":[]}`)
	if env.Code != 40001 || !strings.Contains(env.Msg, "settings_locked") {
		t.Fatalf("锁定时应 settings_locked: %+v", env)
	}
	env = get(t, e, "/v1/mate/settings/workspace")
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("锁定态解析: %v", err)
	}
	if st.Writable || st.LockedBy != "MATE_WORKSPACE_ROOTS" {
		t.Fatalf("锁定态应透出: %+v", st)
	}
}

// put 发一个带 JSON body 的 PUT 请求并解析信封。
func put(t *testing.T, e *gin.Engine, path, body string) *respEnvelope {
	t.Helper()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var env respEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("path=%s 响应非 JSON: %v body=%s", path, err, w.Body.String())
	}
	return &env
}

// jsonString 把路径编成 JSON 字符串（测试里直接拼 body 时避免手写转义）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mustEvalPath 取路径真身（服务端一律按真身比较，测试期望值必须同源）。
func mustEvalPath(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return out
}

// TestWorkspaceFilesRoute "看资料"这个只读面在真实装配下的表现（v14.5 起与名录无关）。
func TestWorkspaceFilesRoute(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "docs")
	lib := filepath.Join(root, "合同归档")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lib, "a.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	e, svc := newEngineWithService(t)

	// 即便配了"起始点偏好"，看资料也不受它限制（v14.5：名录不是边界）。
	if err := planesOf(svc).Workspaces.SetWorkspacePolicy(filepath.Join(root, "tmp"), []string{root}); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}

	// 目录内容：文件与目录都在，且带大小；文件条目**不可**当 cwd。
	env := get(t, e, "/v1/mate/workspaces/files?path="+url.QueryEscape(lib))
	if env.Code != 0 {
		t.Fatalf("files 失败: %+v", env)
	}
	var files dtoworkspace.WorkspaceFilesResp
	if err := json.Unmarshal(env.Data, &files); err != nil {
		t.Fatalf("files data 解析: %v", err)
	}
	if len(files.Entries) != 1 || files.Entries[0].IsDir || files.Entries[0].Size != 3 {
		t.Fatalf("目录内容不符: %+v", files.Entries)
	}
	if env := get(t, e, "/v1/mate/workspaces/files?path="+url.QueryEscape(files.Entries[0].Path)); env.Code == 0 {
		t.Fatalf("把文件当目录列内容应报错: %+v", env)
	}
	// 缺 path：调用方错误（不是回落到默认目录）。
	if env := get(t, e, "/v1/mate/workspaces/files"); env.Code != 40001 {
		t.Fatalf("缺 path 应 40001: %+v", env)
	}
	// 名录外照常可看（v14.5：不再按名录过滤）。
	if env := get(t, e, "/v1/mate/workspaces/files?path="+url.QueryEscape(base)); env.Code != 0 {
		t.Fatalf("名录外目录应可列出内容: %+v", env)
	}
}
