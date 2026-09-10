package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/internal/server/router"
	"github.com/morehao/ark-forge/pkg/config"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// newEngine 走真实装配路径：manager.Service + router.New（含 /v1/forge 分组与默认中间件）。
func newEngine() *gin.Engine {
	e, _ := newEngineWithService()
	return e
}

// newEngineWithService 真实装配并把 Service 交出来：需要先设策略（如工作区根白名单）的用例用它。
func newEngineWithService() (*gin.Engine, *manager.Service) {
	gin.SetMode(gin.TestMode)
	reg := runtime.DefaultRegistry()
	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	h := handler.New(svc, bus)
	return router.New(h), svc
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

// TestRouterModelCatalog 模型目录经真实 Service（无执行面装配）：平台不再内置任何模型清单，
// 目录只能来自运行时——本装配下无 Orchestrator/无连接 → 返回**空目录** + source=none + 说明，
// 这是合法结果（面板据此显示「跟随 Agent 默认」并给出补救提示）。
func TestRouterModelCatalog(t *testing.T) {
	e := newEngine()
	env := get(t, e, "/v1/forge/models")
	if env.Code != 0 {
		t.Fatalf("/models 信封不符: %+v", env)
	}
	var resp struct {
		Agent   string           `json:"agent"`
		Runtime string           `json:"runtime"`
		Source  string           `json:"source"`
		Options []map[string]any `json:"options"`
		Note    string           `json:"note"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("/models data 解析: %v", err)
	}
	if resp.Source != "none" {
		t.Fatalf("无运行时来源应 source=none: %s", resp.Source)
	}
	if len(resp.Options) != 0 {
		t.Fatalf("平台不得内置模型清单，选项应为空: %s", env.Data)
	}
	if resp.Note == "" {
		t.Fatalf("空目录必须带说明: %s", env.Data)
	}
	if resp.Agent != "dsh" {
		t.Fatalf("缺省应取默认 agent(dsh): %s", resp.Agent)
	}

	// 未知 agent → 40401（agent_not_found）。
	env = get(t, e, "/v1/forge/models?agent=nope")
	if env.Code != 40401 {
		t.Fatalf("未知 agent 应 40401: %+v", env)
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
		"POST /v1/forge/agents/:id/default",
		"GET /v1/forge/runtimes",
		"GET /v1/forge/conversations",
		"POST /v1/forge/conversations",
		"GET /v1/forge/conversations/:id/history",
		"POST /v1/forge/conversations/:id/prompt",
		"POST /v1/forge/conversations/:id/cancel",
		"GET /v1/forge/providers",
		"GET /v1/forge/models",
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

// 说明：新增路由**族**时要把前缀加进下面这串过滤条件，否则守卫对它形同虚设；但前缀不能太宽——
// README 正文里的前端页面路由（如 `/settings`）会被一并抓成"未注册路由"，
// 故控制面设置面这条用的是 `/settings/` 而不是 `/settings`。
//
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
			!strings.HasPrefix(p, "/models") && !strings.HasPrefix(p, "/workspaces") &&
			!strings.HasPrefix(p, "/skills") && !strings.HasPrefix(p, "/mcps") &&
			!strings.HasPrefix(p, "/tools") && !strings.HasPrefix(p, "/usage") &&
			!strings.HasPrefix(p, "/search") && !strings.HasPrefix(p, "/settings/") &&
			!strings.HasPrefix(p, "/health") &&
			!strings.HasPrefix(p, "/ws/") {
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

// newAuthEngine 装配带单操作员令牌的引擎（A6）。
func newAuthEngine(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	reg := runtime.DefaultRegistry()
	bus := eventbus.New()
	svc := manager.NewService(reg, bus)
	h := handler.New(svc, bus)
	return router.New(h, router.Options{Token: token})
}

// TestAuthToken 单操作员令牌：/health 免鉴权，其余 REST/WS 路由要求 Bearer 或 ?token=。
func TestAuthToken(t *testing.T) {
	const token = "s3cret-token"
	eng := newAuthEngine(token)

	type probe struct {
		name       string
		method     string
		path       string
		authHeader string
		wantStatus int
		wantCode   int
	}
	cases := []probe{
		{"health 免鉴权", "GET", "/v1/forge/health", "", 200, 0},
		{"缺令牌", "GET", "/v1/forge/agents", "", 401, 40101},
		{"错令牌", "GET", "/v1/forge/agents", "Bearer wrong", 401, 40101},
		{"非 Bearer 方案", "GET", "/v1/forge/agents", "Basic s3cret-token", 401, 40101},
		{"正确 Bearer", "GET", "/v1/forge/agents", "Bearer " + token, 200, 0},
		{"方案大小写不敏感", "GET", "/v1/forge/agents", "bearer " + token, 200, 0},
		{"查询参数（WS/EventSource 兼容）", "GET", "/v1/forge/agents?token=" + token, "", 200, 0},
		{"查询参数错值", "GET", "/v1/forge/agents?token=nope", "", 401, 40101},
		{"POST 也受保护", "POST", "/v1/forge/conversations/export", "", 401, 40101},
		{"WS 无令牌被拦", "GET", "/v1/forge/ws/events", "", 401, 40101},
		// 放行后进入 WS 升级逻辑：非升级请求被升级库拒绝（426 Upgrade Required），
		// 关键是「不再是 401」。
		{"WS 带查询令牌放行（进入升级逻辑）", "GET", "/v1/forge/ws/events?token=" + token, "", 426, 0},
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

// TestAuthTokenDisabled 未配置令牌 = 历史行为（不鉴权）。
func TestAuthTokenDisabled(t *testing.T) {
	eng := newAuthEngine("")
	req := httptest.NewRequest("GET", "/v1/forge/agents", nil)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("未配置令牌应放行: %d %s", w.Code, w.Body.String())
	}
}

// TestWorkspaceRoutes 走真实装配（manager.Service）验证工作空间只读面：
// browse 只列目录且路径与 cwd 同形态；recent 从会话派生（新库为空）。
func TestWorkspaceRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "proj"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := newEngine()

	env := get(t, e, "/v1/forge/workspaces/browse?path="+url.QueryEscape(dir))
	if env.Code != 0 {
		t.Fatalf("browse 失败: %+v", env)
	}
	var browse handler.WorkspaceBrowseResp
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
	if env := get(t, e, "/v1/forge/workspaces/browse?path=relative"); env.Code == 0 {
		t.Fatalf("相对 path 应报错: %+v", env)
	}

	env = get(t, e, "/v1/forge/workspaces/recent")
	if env.Code != 0 {
		t.Fatalf("recent 失败: %+v", env)
	}
	var recent []handler.WorkspaceRecentResp
	if err := json.Unmarshal(env.Data, &recent); err != nil {
		t.Fatalf("recent data 解析: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("新库无会话 → recent 应为空: %+v", recent)
	}

	if env := get(t, e, "/v1/forge/workspaces/recent?limit=abc"); env.Code != 40000 {
		t.Fatalf("非法 limit 应 40000: %+v", env)
	}
}

// TestWorkspaceRoutesWithRoots 工作区根白名单（FORGE_WORKSPACE_ROOTS）在**真实装配**下的对外表现：
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
	e, svc := newEngineWithService()
	if err := svc.SetWorkspacePolicy(filepath.Join(root, "tmp"), []string{root}); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}

	// 缺省起点 = 首个工作区根；到根后不再给上级；roots 回读供面板解释边界。
	env := get(t, e, "/v1/forge/workspaces/browse")
	if env.Code != 0 {
		t.Fatalf("browse 失败: %+v", env)
	}
	var browse handler.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &browse); err != nil {
		t.Fatalf("browse data 解析: %v", err)
	}
	if len(browse.Roots) != 1 || filepath.Base(browse.Path) != "ws" {
		t.Fatalf("应从工作区根起步并回读 roots: %+v", browse)
	}
	if browse.Parent != "" {
		t.Fatalf("到达工作区根后不应再给上级: %q", browse.Parent)
	}

	// 根内子目录：上级 = 根（允许回去）。
	env = get(t, e, "/v1/forge/workspaces/browse?path="+url.QueryEscape(sub))
	if env.Code != 0 {
		t.Fatalf("根内浏览失败: %+v", env)
	}
	var subResp handler.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &subResp); err != nil {
		t.Fatalf("sub data 解析: %v", err)
	}
	if filepath.Base(subResp.Parent) != "ws" {
		t.Fatalf("根内子目录的上级应为根: %+v", subResp)
	}

	// 越界：语义错误码 40001（RestErr 映射），且消息点明是哪个配置在限制。
	env = get(t, e, "/v1/forge/workspaces/browse?path="+url.QueryEscape(outside))
	if env.Code != 40001 {
		t.Fatalf("越界浏览应 40001（不是笼统的 1）: %+v", env)
	}
	if !strings.Contains(env.Msg, "workspace_outside_roots") || !strings.Contains(env.Msg, "FORGE_WORKSPACE_ROOTS") {
		t.Fatalf("越界错误应指明原因与配置键: %q", env.Msg)
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
	e, svc := newEngineWithService()
	if err := svc.SetWorkspacePolicy(provision, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	svc.SetWorkspaceSettingsStore(config.NewSettingsStore(cfgDir, ""))

	// 起步：不限制（restricted=false），可写，来源 = 默认值。
	env := get(t, e, "/v1/forge/settings/workspace")
	if env.Code != 0 {
		t.Fatalf("settings GET 失败: %+v", env)
	}
	var st handler.WorkspaceSettingsResp
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("settings data 解析: %v", err)
	}
	if st.Restricted || !st.Writable || st.Source != "default" || len(st.Roots) != 0 {
		t.Fatalf("起步快照不符: %+v", st)
	}

	// 未画边界时任何可读目录都能浏览（默认的零配置体验不能被这次改造破坏）。
	if env = get(t, e, "/v1/forge/workspaces/browse?path="+url.QueryEscape(outside)); env.Code != 0 {
		t.Fatalf("不限制时根外目录应可浏览: %+v", env)
	}

	// PUT 画边界：生效名单 = 配置的根 + 隐式并入的代建根（顺序：配置项在前）。
	env = put(t, e, "/v1/forge/settings/workspace", `{"roots":[`+jsonString(root)+`]}`)
	if env.Code != 0 {
		t.Fatalf("settings PUT 失败: %+v", env)
	}
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("PUT data 解析: %v", err)
	}
	if !st.Restricted || !st.ProvisionIncluded || len(st.Configured) != 1 || len(st.Roots) != 2 {
		t.Fatalf("更新后快照不符: %+v", st)
	}
	if st.Roots[0] != mustEvalPath(t, root) || filepath.Base(st.Roots[1]) != "tmp" {
		t.Fatalf("生效名单应为「配置根 + 代建根」: %+v", st.Roots)
	}

	// 即时生效：根外被拒（语义码 + 配置键）、代建根本身放行（平台不产出越界会话）。
	env = get(t, e, "/v1/forge/workspaces/browse?path="+url.QueryEscape(outside))
	if env.Code != 40001 || !strings.Contains(env.Msg, "workspace_outside_roots") {
		t.Fatalf("画边界后根外应 40001: %+v", env)
	}
	if _, err := manager.ResolveWorkspace("", provision, svc.WorkspaceRoots(), "conv-s"); err != nil {
		t.Fatalf("代建应仍可用（代建根隐式并入）: %v", err)
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
	env = put(t, e, "/v1/forge/settings/workspace", `{"roots":["relative"]}`)
	if env.Code != 40001 {
		t.Fatalf("非法根应 40001: %+v", env)
	}

	// 被更高优先级来源锁定 → 拒绝写并自报只读（面板据此转只读，而不是假装保存成功）。
	svc.SetWorkspaceSettingsStore(config.NewSettingsStore(cfgDir, config.SourceEnv))
	env = put(t, e, "/v1/forge/settings/workspace", `{"roots":[]}`)
	if env.Code != 40001 || !strings.Contains(env.Msg, "settings_locked") {
		t.Fatalf("锁定时应 settings_locked: %+v", env)
	}
	env = get(t, e, "/v1/forge/settings/workspace")
	if err := json.Unmarshal(env.Data, &st); err != nil {
		t.Fatalf("锁定态解析: %v", err)
	}
	if st.Writable || st.LockedBy != "FORGE_WORKSPACE_ROOTS" {
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

// TestWorkspaceLibrariesAndFilesRoutes 资料库与"看资料"两个只读面在真实装配下的表现。
func TestWorkspaceLibrariesAndFilesRoutes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "docs")
	lib := filepath.Join(root, "合同归档")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lib, "a.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	e, svc := newEngineWithService()

	// 未配白名单 → 资料库为空数组（不是错误：策展能力需要先画边界）。
	env := get(t, e, "/v1/forge/workspaces/libraries")
	if env.Code != 0 {
		t.Fatalf("libraries 失败: %+v", env)
	}
	var libs []handler.WorkspaceLibraryResp
	if err := json.Unmarshal(env.Data, &libs); err != nil {
		t.Fatalf("libraries data 解析: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("未配白名单应无资料库: %+v", libs)
	}

	if err := svc.SetWorkspacePolicy(filepath.Join(root, "tmp"), []string{root}); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	env = get(t, e, "/v1/forge/workspaces/libraries")
	if env.Code != 0 {
		t.Fatalf("libraries 失败: %+v", env)
	}
	if err := json.Unmarshal(env.Data, &libs); err != nil {
		t.Fatalf("libraries data 解析: %v", err)
	}
	if len(libs) != 1 || libs[0].Name != "合同归档" || filepath.Base(libs[0].Root) != "docs" {
		t.Fatalf("资料库应只有 合同归档（代建根 tmp 未创建、且即便创建也被排除）: %+v", libs)
	}

	// 目录内容：文件与目录都在，且带大小；文件条目**不可**当 cwd。
	env = get(t, e, "/v1/forge/workspaces/files?path="+url.QueryEscape(lib))
	if env.Code != 0 {
		t.Fatalf("files 失败: %+v", env)
	}
	var files handler.WorkspaceFilesResp
	if err := json.Unmarshal(env.Data, &files); err != nil {
		t.Fatalf("files data 解析: %v", err)
	}
	if len(files.Entries) != 1 || files.Entries[0].IsDir || files.Entries[0].Size != 3 {
		t.Fatalf("目录内容不符: %+v", files.Entries)
	}
	if env := get(t, e, "/v1/forge/workspaces/files?path="+url.QueryEscape(files.Entries[0].Path)); env.Code == 0 {
		t.Fatalf("把文件当目录列内容应报错: %+v", env)
	}
	// 缺 path：调用方错误（不是回落到默认目录）。
	if env := get(t, e, "/v1/forge/workspaces/files"); env.Code != 40001 {
		t.Fatalf("缺 path 应 40001: %+v", env)
	}
	// 越界 → 与建会话同一个错。
	if env := get(t, e, "/v1/forge/workspaces/files?path="+url.QueryEscape(base)); env.Code != 40001 {
		t.Fatalf("越界应 40001: %+v", env)
	}
}
