package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/gerror"
)

// ——— stub Service（handler.Service 的测试替身，记录调用） ———

type stubSvc struct {
	agents   []handler.AgentResp
	runtimes []handler.RuntimeResp
	sessions []handler.SessionResp

	listAgentsErr   error
	listRuntimesErr error
	listSessionsErr error

	agentErr    error  // P2 管理面桩错误注入
	agentAction string // 最近一次 delete/start/stop 动作（P2 桩）

	providerErr error // P2 provider 桩错误注入
	providers   []handler.ProviderResp

	createCalled bool
	createReq    handler.SessionCreateReq
	createResp   *handler.SessionCreateResp
	createErr    error

	promptCalled bool
	promptID     string
	promptText   string
	promptResp   *handler.SessionPromptResp
	promptErr    error

	cancelCalled bool
	cancelID     string
	cancelErr    error

	historyCalled bool
	historyID     string
	historyAfter  int64
	historyLimit  int
	historyResp   *handler.SessionHistoryResp
	historyErr    error

	memList     []handler.MemoryResp
	revokeID    string
	revokeTitle string
	revokeKind  string
	revokeErr   error

	elicitations        []handler.ElicitationResp
	resolvedElicID      string
	resolvedElicAction  string
	resolvedElicContent map[string]any
	resolveElicErr      error
}

func (s *stubSvc) ListAgents() ([]handler.AgentResp, error) {
	if s.listAgentsErr != nil {
		return nil, s.listAgentsErr
	}
	return s.agents, nil
}

// ——— P2 Agent 管理面桩（Create/Update/Delete/Start/Stop） ———
func (s *stubSvc) CreateAgent(_ context.Context, req handler.AgentCreateReq) (*handler.AgentResp, error) {
	if s.agentErr != nil {
		return nil, s.agentErr
	}
	resp := &handler.AgentResp{ID: req.ID, Name: req.Name, Runtime: req.Runtime, Provider: req.Provider, State: "stopped"}
	s.agents = append(s.agents, *resp)
	return resp, nil
}
func (s *stubSvc) UpdateAgent(_ context.Context, id string, patch handler.AgentUpdateReq) (*handler.AgentResp, error) {
	if s.agentErr != nil {
		return nil, s.agentErr
	}
	return &handler.AgentResp{ID: id, State: "stopped"}, nil
}
func (s *stubSvc) DeleteAgent(_ context.Context, id string) error {
	if s.agentErr != nil {
		return s.agentErr
	}
	s.agentAction = "delete:" + id
	return nil
}
func (s *stubSvc) StartAgent(_ context.Context, id string) error {
	if s.agentErr != nil {
		return s.agentErr
	}
	s.agentAction = "start:" + id
	return nil
}
func (s *stubSvc) StopAgent(_ context.Context, id string) error {
	if s.agentErr != nil {
		return s.agentErr
	}
	s.agentAction = "stop:" + id
	return nil
}
func (s *stubSvc) ListProviders() ([]handler.ProviderResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return s.providers, nil
}
func (s *stubSvc) UpdateProvider(_ context.Context, id string, patch handler.ProviderPatchReq) (*handler.ProviderResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &handler.ProviderResp{ID: id, HasKey: patch.Key != nil && *patch.Key != ""}, nil
}
func (s *stubSvc) ListRuntimes() ([]handler.RuntimeResp, error) {
	if s.listRuntimesErr != nil {
		return nil, s.listRuntimesErr
	}
	return s.runtimes, nil
}
func (s *stubSvc) ListSessions(_ bool) ([]handler.SessionResp, error) {
	if s.listSessionsErr != nil {
		return nil, s.listSessionsErr
	}
	return s.sessions, nil
}
func (s *stubSvc) SearchSessions(_ context.Context, _ string, _ int) ([]handler.SessionSearchHit, error) {
	return nil, nil
}
func (s *stubSvc) GetSession(_ context.Context, id string) (*handler.SessionDetailResp, error) {
	return &handler.SessionDetailResp{
		SessionResp: handler.SessionResp{ID: id},
		Memories:    s.memList,
	}, nil
}
func (s *stubSvc) UpdateSession(_ context.Context, id string, patch handler.SessionPatch) (*handler.SessionResp, error) {
	resp := handler.SessionResp{ID: id}
	if patch.Title != nil {
		resp.Title = *patch.Title
	}
	if patch.Pinned != nil {
		resp.Pinned = *patch.Pinned
	}
	if patch.Archived != nil {
		resp.Archived = *patch.Archived
	}
	return &resp, nil
}
func (s *stubSvc) ListPendingPermissions(_ context.Context, id string) ([]handler.PermissionResp, error) {
	return nil, nil
}
func (s *stubSvc) ResolvePermission(_ context.Context, id, callID, optionID string, always, allowed bool) error {
	return nil
}
func (s *stubSvc) ListPermissionMemories(_ context.Context, id string) ([]handler.MemoryResp, error) {
	return s.memList, nil
}
func (s *stubSvc) RevokeMemory(_ context.Context, id, title, kind string) error {
	s.revokeID = id
	s.revokeTitle = title
	s.revokeKind = kind
	return s.revokeErr
}
func (s *stubSvc) ListPendingElicitations(_ context.Context, id string) ([]handler.ElicitationResp, error) {
	return s.elicitations, nil
}
func (s *stubSvc) ResolveElicitation(_ context.Context, id, elicitationID, action string, content map[string]any) error {
	s.resolvedElicID = elicitationID
	s.resolvedElicAction = action
	s.resolvedElicContent = content
	return s.resolveElicErr
}
func (s *stubSvc) CreateSession(_ context.Context, req handler.SessionCreateReq) (*handler.SessionCreateResp, error) {
	s.createCalled = true
	s.createReq = req
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createResp != nil {
		return s.createResp, nil
	}
	return &handler.SessionCreateResp{ID: "s-new"}, nil
}
func (s *stubSvc) PromptSession(_ context.Context, id, prompt string) (*handler.SessionPromptResp, error) {
	s.promptCalled = true
	s.promptID = id
	s.promptText = prompt
	if s.promptErr != nil {
		return nil, s.promptErr
	}
	if s.promptResp != nil {
		return s.promptResp, nil
	}
	return &handler.SessionPromptResp{ID: id, EventSeq: 5}, nil
}
func (s *stubSvc) CancelTurn(_ context.Context, id string) error {
	s.cancelCalled = true
	s.cancelID = id
	return s.cancelErr
}
func (s *stubSvc) SessionHistory(_ context.Context, id string, after int64, limit int) (*handler.SessionHistoryResp, error) {
	s.historyCalled = true
	s.historyID = id
	s.historyAfter = after
	s.historyLimit = limit
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	if s.historyResp != nil {
		return s.historyResp, nil
	}
	return &handler.SessionHistoryResp{ID: id}, nil
}

// ListRuns 桩：默认空 runs（详情/统计路径不依赖其返回）。
func (s *stubSvc) ListRuns(_ context.Context, id string) ([]handler.RunResp, error) {
	return []handler.RunResp{}, nil
}

// ——— 测试基建 ———

type envelope struct {
	Code      int             `json:"code"`
	RequestID string          `json:"requestID"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
}

// newRestEngine 挂载 handler 的 REST 路由（镜像 router 的路径形态，不经 golib 分组）。
func newRestEngine(svc handler.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.New(svc, nil)
	r := gin.New()
	g := r.Group("/v1/forge")
	g.GET("/agents", h.Agent.List)
	g.POST("/agents", h.Agent.Create)
	g.PATCH("/agents/:id", h.Agent.Update)
	g.DELETE("/agents/:id", h.Agent.Delete)
	g.POST("/agents/:id/start", h.Agent.Start)
	g.POST("/agents/:id/stop", h.Agent.Stop)
	g.GET("/sessions", h.Session.List)
	g.POST("/sessions", h.Session.Create)
	g.GET("/sessions/:id", h.Session.Get)
	g.PATCH("/sessions/:id", h.Session.Update)
	g.GET("/sessions/:id/history", h.Session.History)
	g.POST("/sessions/:id/prompt", h.Session.Prompt)
	g.POST("/sessions/:id/cancel", h.Session.Cancel)
	g.GET("/sessions/:id/permissions", h.Session.Permissions)
	g.POST("/sessions/:id/permission", h.Session.ResolvePermission)
	g.GET("/sessions/:id/permissions/memories", h.Session.PermissionMemories)
	g.POST("/sessions/:id/permissions/memories/revoke", h.Session.RevokeMemory)
	g.GET("/sessions/:id/elicitations", h.Session.Elicitations)
	g.POST("/sessions/:id/elicitation", h.Session.ResolveElicitation)
	g.GET("/runtimes", h.Runtime.List)
	g.GET("/providers", h.Provider.List)
	g.PATCH("/providers/:id", h.Provider.Update)
	return r
}

func do(t *testing.T, r *gin.Engine, method, path, body string) (*envelope, int) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应非信封 JSON: %v body=%s", err, w.Body.String())
	}
	return &env, w.Code
}

// ——— 用例 ———

// TestListEndpoints GET /agents /runtimes /sessions 返回 code=0 + 真实列表载荷。
func TestListEndpoints(t *testing.T) {
	svc := &stubSvc{
		agents: []handler.AgentResp{{ID: "agent-opencode", Name: "opencode-agent", Runtime: "opencode", State: "stopped"}},
		runtimes: []handler.RuntimeResp{
			{Name: "opencode", ID: "opencode", Vers: "opencode acp"},
			{Name: "deepseekharness", ID: "deepseekharness", Vers: "dsh --profile acp"},
		},
		sessions: []handler.SessionResp{{ID: "s1", AgentID: "a", Runtime: "r", Status: "completed"}},
	}
	r := newRestEngine(svc)

	env, _ := do(t, r, "GET", "/v1/forge/agents", "")
	if env.Code != 0 || env.Msg != "success" {
		t.Fatalf("agents 信封不符: %+v", env)
	}
	var agents []handler.AgentResp
	if err := json.Unmarshal(env.Data, &agents); err != nil || len(agents) != 1 || agents[0].ID != "agent-opencode" {
		t.Fatalf("agents data 不符: %s", env.Data)
	}

	env, _ = do(t, r, "GET", "/v1/forge/runtimes", "")
	var rts []handler.RuntimeResp
	if err := json.Unmarshal(env.Data, &rts); err != nil || len(rts) != 2 || rts[1].Name != "deepseekharness" {
		t.Fatalf("runtimes data 不符: %s", env.Data)
	}

	env, _ = do(t, r, "GET", "/v1/forge/sessions", "")
	var sess []handler.SessionResp
	if err := json.Unmarshal(env.Data, &sess); err != nil || len(sess) != 1 || sess[0].Status != "completed" {
		t.Fatalf("sessions data 不符: %s", env.Data)
	}
}

// TestListFailure 列表失败 → code=1（handler 层兜底）。
func TestListFailure(t *testing.T) {
	svc := &stubSvc{listAgentsErr: errors.New("boom")}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/agents", "")
	if env.Code != 1 {
		t.Fatalf("期望 code=1，got %+v", env)
	}
}

// TestCreateSession 正常创建：解析 body → 透传 svc → 信封含 id/event_seq。
func TestCreateSession(t *testing.T) {
	svc := &stubSvc{createResp: &handler.SessionCreateResp{ID: "s-abc", AgentID: "agent-opencode", Runtime: "opencode", Status: "in_progress", EventSeq: 9}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/sessions", `{"agent_id":"agent-opencode","prompt":"hi","cwd":"/tmp"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var resp struct {
		ID       string `json:"id"`
		EventSeq int64  `json:"event_seq"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil || resp.ID != "s-abc" || resp.EventSeq != 9 {
		t.Fatalf("create data 不符: %s", env.Data)
	}
	if !svc.createCalled || svc.createReq.AgentID != "agent-opencode" || svc.createReq.Prompt != "hi" || svc.createReq.Cwd != "/tmp" {
		t.Fatalf("create 未正确透传: %+v", svc.createReq)
	}
}

// TestCreateSessionBadBody 缺 agent_id（binding required）→ code 40000。
func TestCreateSessionBadBody(t *testing.T) {
	r := newRestEngine(&stubSvc{})
	env, _ := do(t, r, "POST", "/v1/forge/sessions", `{"prompt":"hi"}`)
	if env.Code != 40000 {
		t.Fatalf("期望 40000，got %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/forge/sessions", `not-json{`)
	if env.Code != 40000 {
		t.Fatalf("非 JSON 期望 40000，got %+v", env)
	}
}

// TestCreateSessionServiceError svc 业务错误原样透出（40901 忙）。
func TestCreateSessionServiceError(t *testing.T) {
	svc := &stubSvc{createErr: gerror.Error{Code: 40901, Msg: "turn in progress"}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/sessions", `{"agent_id":"a","prompt":"hi"}`)
	if env.Code != 40901 || !strings.Contains(env.Msg, "turn in progress") {
		t.Fatalf("服务错误未透出: %+v", env)
	}
}

// TestPromptAndCancel prompt（校验必填）/cancel 调用透传。
func TestPromptAndCancel(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	// prompt 缺 prompt → 40000。
	env, _ := do(t, r, "POST", "/v1/forge/sessions/s1/prompt", `{}`)
	if env.Code != 40000 {
		t.Fatalf("prompt 缺参期望 40000，got %+v", env)
	}
	// 正常 prompt。
	env, _ = do(t, r, "POST", "/v1/forge/sessions/s1/prompt", `{"prompt":"再来一轮"}`)
	if env.Code != 0 || !svc.promptCalled || svc.promptID != "s1" || svc.promptText != "再来一轮" {
		t.Fatalf("prompt 调用不符: called=%v id=%s env=%+v", svc.promptCalled, svc.promptID, env)
	}
	var presp struct {
		ID       string `json:"id"`
		EventSeq int64  `json:"event_seq"`
	}
	if err := json.Unmarshal(env.Data, &presp); err != nil || presp.EventSeq != 5 {
		t.Fatalf("prompt data 不符: %s", env.Data)
	}

	// cancel。
	env, _ = do(t, r, "POST", "/v1/forge/sessions/s1/cancel", "")
	if env.Code != 0 || !svc.cancelCalled || svc.cancelID != "s1" {
		t.Fatalf("cancel 调用不符: env=%+v called=%v", env, svc.cancelCalled)
	}
	var cdata struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Data, &cdata); err != nil || cdata.Status != "cancelling" {
		t.Fatalf("cancel data 不符: %s", env.Data)
	}
}

// TestHistoryParamParsing history 分页参数解析与夹取：默认/显式/非法。
func TestHistoryParamParsing(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	// 默认 after=0 limit=200。
	do(t, r, "GET", "/v1/forge/sessions/s1/history", "")
	if !svc.historyCalled || svc.historyID != "s1" || svc.historyAfter != 0 || svc.historyLimit != 200 {
		t.Fatalf("默认参数不符: %+v %d %d", svc.historyID, svc.historyAfter, svc.historyLimit)
	}

	// 显式 after/limit。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/sessions/s1/history?after=7&limit=50", "")
	if svc.historyAfter != 7 || svc.historyLimit != 50 {
		t.Fatalf("显式参数不符: after=%d limit=%d", svc.historyAfter, svc.historyLimit)
	}

	// limit 上限 1000 夹取。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/sessions/s1/history?limit=999999", "")
	if svc.historyLimit != 1000 {
		t.Fatalf("limit 应夹到 1000，got %d", svc.historyLimit)
	}

	// limit=0 → 1。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/sessions/s1/history?limit=0", "")
	if svc.historyLimit != 1 {
		t.Fatalf("limit=0 应夹到 1，got %d", svc.historyLimit)
	}

	// 非法 after → 40000。
	svc.historyCalled = false
	env, _ := do(t, r, "GET", "/v1/forge/sessions/s1/history?after=-1", "")
	if env.Code != 40000 {
		t.Fatalf("负 after 期望 40000，got %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/forge/sessions/s1/history?after=abc", "")
	if env.Code != 40000 {
		t.Fatalf("非数字 after 期望 40000，got %+v", env)
	}
	if svc.historyCalled {
		t.Fatal("非法参数不应触达 svc")
	}
}

// TestHistorySuccess 正常 history 返回事件数组（含 has_more）。
func TestHistorySuccess(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/sessions/s1/history?after=1&limit=10", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out struct {
		ID      string `json:"id"`
		HasMore bool   `json:"has_more"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil || out.ID != "s1" {
		t.Fatalf("history data 不符: %s", env.Data)
	}
}

// TestPermissionMemoriesList GET /sessions/:id/permissions/memories 返回会话级权限记忆（含 decision 侧）。
func TestPermissionMemoriesList(t *testing.T) {
	svc := &stubSvc{memList: []handler.MemoryResp{
		{Title: "运行演示命令 echo hello", Kind: "execute", Decision: "allow", Since: "2026-01-02T03:04:05Z"},
		{Title: "删除文件", Kind: "execute", Decision: "reject", Since: "2026-01-03T03:04:05Z"},
	}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/sessions/s1/permissions/memories", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []handler.MemoryResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 2 ||
		out[0].Title != "运行演示命令 echo hello" || out[0].Kind != "execute" || out[0].Decision != "allow" ||
		out[1].Decision != "reject" {
		t.Fatalf("memories data 不符: %s", env.Data)
	}
}

// TestRevokeMemory 单条撤销（title+kind）与一键撤销（均空）都正确透传。
func TestRevokeMemory(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	env, _ := do(t, r, "POST", "/v1/forge/sessions/s1/permissions/memories/revoke",
		`{"title":"运行演示命令 echo hello","kind":"execute"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.revokeID != "s1" || svc.revokeTitle != "运行演示命令 echo hello" || svc.revokeKind != "execute" {
		t.Fatalf("撤销单条透传不符: %+v", svc)
	}

	env, _ = do(t, r, "POST", "/v1/forge/sessions/s1/permissions/memories/revoke", `{}`)
	if env.Code != 0 {
		t.Fatalf("一键撤销信封不符: %+v", env)
	}
	if svc.revokeTitle != "" || svc.revokeKind != "" {
		t.Fatalf("一键撤销应传空 title/kind: %+v", svc)
	}
}

// TestElicitationList GET /sessions/:id/elicitations 返回挂起结构化提问（表单 schema 透传）。
func TestElicitationList(t *testing.T) {
	svc := &stubSvc{elicitations: []handler.ElicitationResp{{
		ElicitationID: "el-abc", SessionID: "s1", Mode: "form",
		Message: "请选择一个环境", TimeoutAt: "2026-01-02T03:04:05Z",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}}}},
	}}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/sessions/s1/elicitations", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []handler.ElicitationResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 || out[0].ElicitationID != "el-abc" || out[0].Mode != "form" {
		t.Fatalf("elicitations data 不符: %s", env.Data)
	}
}

// TestResolveElicitation POST /sessions/:id/elicitation 决策透传（accept + content）。
func TestResolveElicitation(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/sessions/s1/elicitation",
		`{"elicitation_id":"el-abc","action":"accept","content":{"env":"prod"}}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.resolvedElicID != "el-abc" || svc.resolvedElicAction != "accept" || svc.resolvedElicContent["env"] != "prod" {
		t.Fatalf("决策透传不符: %+v", svc)
	}
}

// TestResolveElicitationBadBody 缺 elicitation_id/action → code 40000。
func TestResolveElicitationBadBody(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/sessions/s1/elicitation", `{"action":"accept"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 elicitation_id 应 40000: %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/forge/sessions/s1/elicitation", `{"elicitation_id":"el-abc"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 action 应 40000: %+v", env)
	}
}

// TestAgentManagementRoutes P2 管理面路由：注册/启停/热更新/删除经 stub 桩往返。
func TestAgentManagementRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	env, code := do(t, r, "POST", "/v1/forge/agents", `{"id":"alias-echo","name":"别名echo","runtime":"echo"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("注册失败 code=%d env=%d msg=%s", code, env.Code, env.Msg)
	}
	env, code = do(t, r, "POST", "/v1/forge/agents/alias-echo/start", "")
	if code != 200 || env.Code != 0 || svc.agentAction != "start:alias-echo" {
		t.Fatalf("start 失败 code=%d env=%d action=%s", code, env.Code, svc.agentAction)
	}
	env, code = do(t, r, "POST", "/v1/forge/agents/alias-echo/stop", "")
	if code != 200 || env.Code != 0 || svc.agentAction != "stop:alias-echo" {
		t.Fatalf("stop 失败 code=%d env=%d action=%s", code, env.Code, svc.agentAction)
	}
	env, code = do(t, r, "PATCH", "/v1/forge/agents/alias-echo", `{"name":"别名echo v2"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH 失败 code=%d env=%d", code, env.Code)
	}
	env, code = do(t, r, "DELETE", "/v1/forge/agents/alias-echo", "")
	if code != 200 || env.Code != 0 || svc.agentAction != "delete:alias-echo" {
		t.Fatalf("delete 失败 code=%d env=%d action=%s", code, env.Code, svc.agentAction)
	}
}

// TestProviderRoutes provider 注入面路由：GET 列表 + PATCH upsert（密钥只写不读回）。
func TestProviderRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, code := do(t, r, "PATCH", "/v1/forge/providers/default", `{"type":"openai","model":"gpt-4o","key":"sk-x"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH provider 失败 code=%d env=%d", code, env.Code)
	}
	env, code = do(t, r, "GET", "/v1/forge/providers", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET providers 失败 code=%d env=%d", code, env.Code)
	}
}
