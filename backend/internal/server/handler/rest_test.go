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

	alwaysList  []handler.AlwaysAllowResp
	revokeID    string
	revokeTitle string
	revokeKind  string
	revokeErr   error
}

func (s *stubSvc) ListAgents() ([]handler.AgentResp, error) {
	if s.listAgentsErr != nil {
		return nil, s.listAgentsErr
	}
	return s.agents, nil
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
		SessionResp:   handler.SessionResp{ID: id},
		AlwaysAllowed: s.alwaysList,
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
func (s *stubSvc) RevokeAlways(_ context.Context, id, title, kind string) error {
	s.revokeID = id
	s.revokeTitle = title
	s.revokeKind = kind
	return s.revokeErr
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
	g.GET("/sessions", h.Session.List)
	g.POST("/sessions", h.Session.Create)
	g.GET("/sessions/:id", h.Session.Get)
	g.PATCH("/sessions/:id", h.Session.Update)
	g.GET("/sessions/:id/history", h.Session.History)
	g.POST("/sessions/:id/prompt", h.Session.Prompt)
	g.POST("/sessions/:id/cancel", h.Session.Cancel)
	g.GET("/sessions/:id/permissions", h.Session.Permissions)
	g.POST("/sessions/:id/permission", h.Session.ResolvePermission)
	g.GET("/sessions/:id/permissions/always", h.Session.AlwaysAllowed)
	g.POST("/sessions/:id/permissions/always/revoke", h.Session.RevokeAlways)
	g.GET("/runtimes", h.Runtime.List)
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

// TestAlwaysAllowedList GET /sessions/:id/permissions/always 返回会话级「始终允许」记忆。
func TestAlwaysAllowedList(t *testing.T) {
	svc := &stubSvc{alwaysList: []handler.AlwaysAllowResp{
		{Title: "运行演示命令 echo hello", Kind: "execute", Since: "2026-01-02T03:04:05Z"},
	}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/sessions/s1/permissions/always", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []handler.AlwaysAllowResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 || out[0].Title != "运行演示命令 echo hello" || out[0].Kind != "execute" {
		t.Fatalf("always data 不符: %s", env.Data)
	}
}

// TestRevokeAlways 单条撤销（title+kind）与一键撤销（均空）都正确透传。
func TestRevokeAlways(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	env, _ := do(t, r, "POST", "/v1/forge/sessions/s1/permissions/always/revoke",
		`{"title":"运行演示命令 echo hello","kind":"execute"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.revokeID != "s1" || svc.revokeTitle != "运行演示命令 echo hello" || svc.revokeKind != "execute" {
		t.Fatalf("撤销单条透传不符: %+v", svc)
	}

	env, _ = do(t, r, "POST", "/v1/forge/sessions/s1/permissions/always/revoke", `{}`)
	if env.Code != 0 {
		t.Fatalf("一键撤销信封不符: %+v", env)
	}
	if svc.revokeTitle != "" || svc.revokeKind != "" {
		t.Fatalf("一键撤销应传空 title/kind: %+v", svc)
	}
}
