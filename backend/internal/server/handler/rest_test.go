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
	agents        []handler.AgentResp
	runtimes      []handler.RuntimeResp
	conversations []handler.ConversationResp

	listAgentsErr        error
	listRuntimesErr      error
	listConversationsErr error

	agentErr    error  // P2 管理面桩错误注入
	agentAction string // 最近一次 delete/start/stop 动作（P2 桩）

	providerErr error // P2 provider 桩错误注入
	providers   []handler.ProviderResp

	createCalled bool
	createReq    handler.ConversationCreateReq
	createResp   *handler.ConversationCreateResp
	createErr    error

	promptCalled bool
	promptID     string
	promptText   string
	promptResp   *handler.ConversationPromptResp
	promptErr    error

	cancelCalled bool
	cancelID     string
	cancelErr    error

	historyCalled bool
	historyID     string
	historyAfter  int64
	historyLimit  int
	historyResp   *handler.ConversationHistoryResp
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

	skills []handler.SkillResp // P1 能力数据面桩
	mcps   []handler.McpServerResp
	tools  []handler.ToolResp
	capErr error // 能力列表桩错误注入

	agentCapResp *handler.AgentCapabilitiesResp // P2 §8.1 统一能力目录桩
	agentCapErr  error
	agentCapID   string
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
	resp := &handler.AgentResp{ID: req.ID, Name: req.Name, Provider: req.Provider, State: "stopped"}
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

// ——— P1 能力数据面桩（ListSkills/ListMcpServers/ListTools） ———
func (s *stubSvc) ListSkills() ([]handler.SkillResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.skills, nil
}
func (s *stubSvc) ListMcpServers() ([]handler.McpServerResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.mcps, nil
}
func (s *stubSvc) ListTools() ([]handler.ToolResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.tools, nil
}
func (s *stubSvc) AgentCapabilities(id string) (*handler.AgentCapabilitiesResp, error) {
	s.agentCapID = id
	if s.agentCapErr != nil {
		return nil, s.agentCapErr
	}
	if s.agentCapResp != nil {
		return s.agentCapResp, nil
	}
	return &handler.AgentCapabilitiesResp{AgentID: id}, nil
}
func (s *stubSvc) ListRuntimes() ([]handler.RuntimeResp, error) {
	if s.listRuntimesErr != nil {
		return nil, s.listRuntimesErr
	}
	return s.runtimes, nil
}
func (s *stubSvc) ListConversations(_ bool) ([]handler.ConversationResp, error) {
	if s.listConversationsErr != nil {
		return nil, s.listConversationsErr
	}
	return s.conversations, nil
}
func (s *stubSvc) SearchConversations(_ context.Context, _ string, _ int) ([]handler.ConversationSearchHit, error) {
	return nil, nil
}
func (s *stubSvc) GetConversation(_ context.Context, id string) (*handler.ConversationDetailResp, error) {
	return &handler.ConversationDetailResp{
		ConversationResp: handler.ConversationResp{ID: id},
		Memories:         s.memList,
	}, nil
}
func (s *stubSvc) UpdateConversation(_ context.Context, id string, patch handler.ConversationPatch) (*handler.ConversationResp, error) {
	resp := handler.ConversationResp{ID: id}
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
func (s *stubSvc) CreateConversation(_ context.Context, req handler.ConversationCreateReq) (*handler.ConversationCreateResp, error) {
	s.createCalled = true
	s.createReq = req
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createResp != nil {
		return s.createResp, nil
	}
	return &handler.ConversationCreateResp{ID: "s-new"}, nil
}
func (s *stubSvc) PromptConversation(_ context.Context, id, prompt string) (*handler.ConversationPromptResp, error) {
	s.promptCalled = true
	s.promptID = id
	s.promptText = prompt
	if s.promptErr != nil {
		return nil, s.promptErr
	}
	if s.promptResp != nil {
		return s.promptResp, nil
	}
	return &handler.ConversationPromptResp{ID: id, EventSeq: 5}, nil
}
func (s *stubSvc) CancelTurn(_ context.Context, id string) error {
	s.cancelCalled = true
	s.cancelID = id
	return s.cancelErr
}
func (s *stubSvc) ConversationHistory(_ context.Context, id string, after int64, limit int) (*handler.ConversationHistoryResp, error) {
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
	return &handler.ConversationHistoryResp{ID: id}, nil
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
	g.GET("/conversations", h.Conversation.List)
	g.POST("/conversations", h.Conversation.Create)
	g.GET("/conversations/:id", h.Conversation.Get)
	g.PATCH("/conversations/:id", h.Conversation.Update)
	g.GET("/conversations/:id/history", h.Conversation.History)
	g.POST("/conversations/:id/prompt", h.Conversation.Prompt)
	g.POST("/conversations/:id/cancel", h.Conversation.Cancel)
	g.GET("/conversations/:id/permissions", h.Conversation.Permissions)
	g.POST("/conversations/:id/permission", h.Conversation.ResolvePermission)
	g.GET("/conversations/:id/permissions/memories", h.Conversation.PermissionMemories)
	g.POST("/conversations/:id/permissions/memories/revoke", h.Conversation.RevokeMemory)
	g.GET("/conversations/:id/elicitations", h.Conversation.Elicitations)
	g.POST("/conversations/:id/elicitation", h.Conversation.ResolveElicitation)
	g.GET("/runtimes", h.Runtime.List)
	g.GET("/providers", h.Provider.List)
	g.PATCH("/providers/:id", h.Provider.Update)
	g.GET("/skills", h.Capability.ListSkills)
	g.GET("/mcps", h.Capability.ListMcpServers)
	g.GET("/agents/:id/capabilities", h.Capability.AgentCapabilities)
	g.GET("/tools", h.Capability.ListTools)
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

// TestListEndpoints GET /agents /runtimes /conversations 返回 code=0 + 真实列表载荷。
func TestListEndpoints(t *testing.T) {
	svc := &stubSvc{
		agents: []handler.AgentResp{{ID: "agent-opencode", Name: "opencode-agent", State: "stopped"}},
		runtimes: []handler.RuntimeResp{
			{Name: "opencode", ID: "opencode", Vers: "opencode acp"},
			{Name: "deepseekharness", ID: "deepseekharness", Vers: "dsh --profile acp"},
		},
		conversations: []handler.ConversationResp{{ID: "s1", AgentID: "a", Status: "completed"}},
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

	env, _ = do(t, r, "GET", "/v1/forge/conversations", "")
	var sess []handler.ConversationResp
	if err := json.Unmarshal(env.Data, &sess); err != nil || len(sess) != 1 || sess[0].Status != "completed" {
		t.Fatalf("conversations data 不符: %s", env.Data)
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

// TestCreateConversation 正常创建：解析 body → 透传 svc → 信封含 id/event_seq。
func TestCreateConversation(t *testing.T) {
	svc := &stubSvc{createResp: &handler.ConversationCreateResp{ID: "s-abc", AgentID: "agent-opencode", Status: "in_progress", EventSeq: 9}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/conversations", `{"agent_id":"agent-opencode","prompt":"hi","cwd":"/tmp"}`)
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

// TestCreateConversationBadBody 非 JSON 请求体 → code 40000（agent_id 现为可选，不再校验缺失）。
func TestCreateConversationBadBody(t *testing.T) {
	r := newRestEngine(&stubSvc{})
	env, _ := do(t, r, "POST", "/v1/forge/conversations", `not-json{`)
	if env.Code != 40000 {
		t.Fatalf("非 JSON 期望 40000，got %+v", env)
	}
}

// TestCreateConversationOptionalAgent 省略 agent_id 应被接受并透传给 service（默认在 service 层解析）。
func TestCreateConversationOptionalAgent(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/conversations", `{"prompt":"hi"}`)
	if env.Code != 0 {
		t.Fatalf("省略 agent_id 应被接受: %+v", env)
	}
	if !svc.createCalled || svc.createReq.AgentID != "" || svc.createReq.Prompt != "hi" {
		t.Fatalf("create 未正确透传（agent_id 应为空）: %+v", svc.createReq)
	}
}

// TestCreateConversationServiceError svc 业务错误原样透出（40901 忙）。
func TestCreateConversationServiceError(t *testing.T) {
	svc := &stubSvc{createErr: gerror.Error{Code: 40901, Msg: "turn in progress"}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/conversations", `{"agent_id":"a","prompt":"hi"}`)
	if env.Code != 40901 || !strings.Contains(env.Msg, "turn in progress") {
		t.Fatalf("服务错误未透出: %+v", env)
	}
}

// TestPromptAndCancel prompt（校验必填）/cancel 调用透传。
func TestPromptAndCancel(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	// prompt 缺 prompt → 40000。
	env, _ := do(t, r, "POST", "/v1/forge/conversations/s1/prompt", `{}`)
	if env.Code != 40000 {
		t.Fatalf("prompt 缺参期望 40000，got %+v", env)
	}
	// 正常 prompt。
	env, _ = do(t, r, "POST", "/v1/forge/conversations/s1/prompt", `{"prompt":"再来一轮"}`)
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
	env, _ = do(t, r, "POST", "/v1/forge/conversations/s1/cancel", "")
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
	do(t, r, "GET", "/v1/forge/conversations/s1/history", "")
	if !svc.historyCalled || svc.historyID != "s1" || svc.historyAfter != 0 || svc.historyLimit != 200 {
		t.Fatalf("默认参数不符: %+v %d %d", svc.historyID, svc.historyAfter, svc.historyLimit)
	}

	// 显式 after/limit。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?after=7&limit=50", "")
	if svc.historyAfter != 7 || svc.historyLimit != 50 {
		t.Fatalf("显式参数不符: after=%d limit=%d", svc.historyAfter, svc.historyLimit)
	}

	// limit 上限 1000 夹取。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?limit=999999", "")
	if svc.historyLimit != 1000 {
		t.Fatalf("limit 应夹到 1000，got %d", svc.historyLimit)
	}

	// limit=0 → 1。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?limit=0", "")
	if svc.historyLimit != 1 {
		t.Fatalf("limit=0 应夹到 1，got %d", svc.historyLimit)
	}

	// 非法 after → 40000。
	svc.historyCalled = false
	env, _ := do(t, r, "GET", "/v1/forge/conversations/s1/history?after=-1", "")
	if env.Code != 40000 {
		t.Fatalf("负 after 期望 40000，got %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/forge/conversations/s1/history?after=abc", "")
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
	env, _ := do(t, r, "GET", "/v1/forge/conversations/s1/history?after=1&limit=10", "")
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

// TestPermissionMemoriesList GET /conversations/:id/permissions/memories 返回会话级权限记忆（含 decision 侧）。
func TestPermissionMemoriesList(t *testing.T) {
	svc := &stubSvc{memList: []handler.MemoryResp{
		{Title: "运行演示命令 echo hello", Kind: "execute", Decision: "allow", Since: "2026-01-02T03:04:05Z"},
		{Title: "删除文件", Kind: "execute", Decision: "reject", Since: "2026-01-03T03:04:05Z"},
	}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/conversations/s1/permissions/memories", "")
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

	env, _ := do(t, r, "POST", "/v1/forge/conversations/s1/permissions/memories/revoke",
		`{"title":"运行演示命令 echo hello","kind":"execute"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.revokeID != "s1" || svc.revokeTitle != "运行演示命令 echo hello" || svc.revokeKind != "execute" {
		t.Fatalf("撤销单条透传不符: %+v", svc)
	}

	env, _ = do(t, r, "POST", "/v1/forge/conversations/s1/permissions/memories/revoke", `{}`)
	if env.Code != 0 {
		t.Fatalf("一键撤销信封不符: %+v", env)
	}
	if svc.revokeTitle != "" || svc.revokeKind != "" {
		t.Fatalf("一键撤销应传空 title/kind: %+v", svc)
	}
}

// TestElicitationList GET /conversations/:id/elicitations 返回挂起结构化提问（表单 schema 透传）。
func TestElicitationList(t *testing.T) {
	svc := &stubSvc{elicitations: []handler.ElicitationResp{{
		ElicitationID: "el-abc", SessionID: "s1", Mode: "form",
		Message: "请选择一个环境", TimeoutAt: "2026-01-02T03:04:05Z",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}}}},
	}}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "GET", "/v1/forge/conversations/s1/elicitations", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []handler.ElicitationResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 || out[0].ElicitationID != "el-abc" || out[0].Mode != "form" {
		t.Fatalf("elicitations data 不符: %s", env.Data)
	}
}

// TestResolveElicitation POST /conversations/:id/elicitation 决策透传（accept + content）。
func TestResolveElicitation(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, _ := do(t, r, "POST", "/v1/forge/conversations/s1/elicitation",
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
	env, _ := do(t, r, "POST", "/v1/forge/conversations/s1/elicitation", `{"action":"accept"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 elicitation_id 应 40000: %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/forge/conversations/s1/elicitation", `{"elicitation_id":"el-abc"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 action 应 40000: %+v", env)
	}
}

// TestAgentManagementRoutes P2 管理面路由：注册/启停/热更新/删除经 stub 桩往返。
func TestAgentManagementRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	env, code := do(t, r, "POST", "/v1/forge/agents", `{"id":"alias-echo","name":"别名echo","runtimes":["echo"]}`)
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

// TestCapabilityRoutes P1 能力数据面路由：GET /skills /mcps /tools 列出目录（信封 data 数组）。
func TestCapabilityRoutes(t *testing.T) {
	svc := &stubSvc{
		skills: []handler.SkillResp{{ID: "plan", Name: "plan", Description: "先规划再动手"}},
		mcps:   []handler.McpServerResp{{ID: "fs", Transport: "stdio", Command: "npx"}},
		tools:  []handler.ToolResp{{Name: "web_search", Source: "builtin", Description: "联网搜索"}},
	}
	r := newRestEngine(svc)

	env, code := do(t, r, "GET", "/v1/forge/skills", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET skills 失败 code=%d env=%d", code, env.Code)
	}
	var skills []handler.SkillResp
	if err := json.Unmarshal(env.Data, &skills); err != nil || len(skills) != 1 || skills[0].ID != "plan" {
		t.Fatalf("skills data 不符: %s", env.Data)
	}

	env, code = do(t, r, "GET", "/v1/forge/mcps", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET mcps 失败 code=%d env=%d", code, env.Code)
	}
	var mcps []handler.McpServerResp
	if err := json.Unmarshal(env.Data, &mcps); err != nil || len(mcps) != 1 || mcps[0].ID != "fs" {
		t.Fatalf("mcps data 不符: %s", env.Data)
	}

	env, code = do(t, r, "GET", "/v1/forge/tools", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET tools 失败 code=%d env=%d", code, env.Code)
	}
	var tools []handler.ToolResp
	if err := json.Unmarshal(env.Data, &tools); err != nil || len(tools) != 1 || tools[0].Name != "web_search" {
		t.Fatalf("tools data 不符: %s", env.Data)
	}
}

// TestAgentCapabilitiesRoute P2 §8.1 统一能力目录路由：GET /agents/:id/capabilities 返回
// 聚合视图（信封 data 对象）；未知 agent 透传 40401。
func TestAgentCapabilitiesRoute(t *testing.T) {
	svc := &stubSvc{
		agentCapResp: &handler.AgentCapabilitiesResp{
			AgentID:  "a1",
			Runtimes: []string{"opencode"},
			Skills:   []handler.AgentSkillResp{{ID: "plan", Name: "plan"}},
			McpServers: []handler.AgentMcpServerResp{
				{ID: "fs", Transport: "stdio", Enabled: true, Runtimes: []string{"opencode"}},
			},
			Tools: []handler.ToolResp{{Name: "web_search", Source: "builtin"}},
			Notes: []string{"候选 runtime \"ghost\" 未注册（按声明过滤，不推导其能力）"},
		},
	}
	r := newRestEngine(svc)

	env, code := do(t, r, "GET", "/v1/forge/agents/a1/capabilities", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET agent capabilities 失败 code=%d env=%d", code, env.Code)
	}
	var got handler.AgentCapabilitiesResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v (%s)", err, env.Data)
	}
	if got.AgentID != "a1" || svc.agentCapID != "a1" {
		t.Fatalf("agent id 不符: %+v stub=%q", got, svc.agentCapID)
	}
	if len(got.Skills) != 1 || len(got.McpServers) != 1 || !got.McpServers[0].Enabled || len(got.Tools) != 1 {
		t.Fatalf("聚合视图不符: %+v", got)
	}
	if len(got.Notes) != 1 {
		t.Fatalf("notes 不符: %+v", got.Notes)
	}

	// 未知 agent：Service 返回 40401 → 信封透传（HTTP 200 + code≠0）。
	svc.agentCapErr = gerror.Error{Code: 40401, Msg: `未知 agent "nope"`}
	env, code = do(t, r, "GET", "/v1/forge/agents/nope/capabilities", "")
	if code != 200 || env.Code != 40401 {
		t.Fatalf("未知 agent 应 40401，got http=%d code=%d", code, env.Code)
	}
}
