package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/acl"
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

	diagID   string
	diagResp *handler.AgentDiagnosticsResp
	diagErr  error

	searchMsgQuery string
	searchMsgLimit int
	searchMsgResp  *handler.MessageSearchResp
	searchMsgErr   error

	exportReq  handler.ExportReq
	exportResp *handler.ExportResp
	exportErr  error

	agentErr        error                           // P2 管理面桩错误注入
	resolvePermErr  *gerror.Error                   // 权限决策桩错误注入
	resolvePermResp *handler.PermissionDecisionResp // 权限决策桩回执（nil = 默认构造）
	agentAction     string                          // 最近一次 delete/start/stop 动作（P2 桩）

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

	historyCalled    bool
	historyID        string
	historyAfter     int64
	historyBefore    int64
	historyHasBefore bool
	historyLimit     int
	historyResp      *handler.ConversationHistoryResp
	historyErr       error

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

	updateAgentResp *handler.AgentResp     // PATCH /agents/:id 桩
	updateAgentReq  handler.AgentUpdateReq // 捕获 PATCH 入参（策略透传断言）
	updateAgentErr  error
	probeResp       *handler.McpProbeResp // P2 §5.2 MCP live 探测桩
	probeErr        error
	probeID         string

	callMcpResp *handler.McpToolCallResp // B1 tools/call 桩
	callMcpErr  error
	callMcpID   string
	callMcpTool string
	callMcpReq  handler.McpToolCallReq

	mcpLiveID   string // B2 资源/提示词桩
	mcpLiveURI  string
	mcpLiveName string
	mcpLiveArgs map[string]string
	mcpLiveErr  error

	usageResp  *handler.UsageSummaryResp // M3 GET /usage/summary 桩
	usageQuery handler.UsageQueryReq     // 捕获查询参数
	usageErr   error
}

// RestErr 桩：与 manager 一致——已映射错误透传，acl 错误按分类映射（测试只关心 acl→信封码）。
func (s *stubSvc) RestErr(err error) error {
	if err == nil {
		return nil
	}
	var ge gerror.Error
	if errors.As(err, &ge) {
		return err
	}
	var ae *acl.Error
	if errors.As(err, &ae) {
		code := 40001
		switch ae.Class {
		case acl.ClassUnsupported:
			code = 40501
		case acl.ClassLimit:
			code = 40901
		case acl.ClassService, acl.ClassConnection:
			code = 50201
		}
		if ae.Code == "agent_not_found" || ae.Code == "conversation_not_found" || ae.Code == "runtime_not_found" || ae.Code == "mcp_not_found" {
			code = 40401
		}
		return gerror.Error{Code: code, Msg: ae.Details}
	}
	return gerror.Error{Code: 50000, Msg: err.Error()}
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
	s.updateAgentReq = patch
	if s.agentErr != nil {
		return nil, s.agentErr
	}
	if s.updateAgentErr != nil {
		return nil, s.updateAgentErr
	}
	if s.updateAgentResp != nil {
		return s.updateAgentResp, nil
	}
	return &handler.AgentResp{ID: id, State: "stopped"}, nil
}
func (s *stubSvc) SetDefaultAgent(_ context.Context, id string) (*handler.AgentResp, error) {
	if s.agentErr != nil {
		return nil, s.agentErr
	}
	s.agentAction = "default:" + id
	return &handler.AgentResp{ID: id, State: "stopped", IsDefault: true}, nil
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
func (s *stubSvc) ModelCatalog(_ context.Context, agentID string) (*handler.ModelCatalogResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &handler.ModelCatalogResp{Agent: agentID, Source: "none"}, nil
}
func (s *stubSvc) DefaultAgentID() string { return "dsh" }

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
func (s *stubSvc) UsageSummary(_ context.Context, q handler.UsageQueryReq) (*handler.UsageSummaryResp, error) {
	s.usageQuery = q
	if s.usageErr != nil {
		return nil, s.usageErr
	}
	if s.usageResp != nil {
		return s.usageResp, nil
	}
	return &handler.UsageSummaryResp{GroupBy: "agent", Source: "sqlite", Buckets: []handler.UsageBucketResp{}}, nil
}
func (s *stubSvc) CallMcpTool(_ context.Context, id, tool string, req handler.McpToolCallReq) (*handler.McpToolCallResp, error) {
	s.callMcpID, s.callMcpTool, s.callMcpReq = id, tool, req
	if s.callMcpErr != nil {
		return nil, s.callMcpErr
	}
	if s.callMcpResp != nil {
		return s.callMcpResp, nil
	}
	return &handler.McpToolCallResp{ServerID: id, Tool: tool, Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}, Text: "ok"}, nil
}
func (s *stubSvc) ListMcpResources(_ context.Context, id string) (*handler.McpResourcesResp, error) {
	s.mcpLiveID = id
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &handler.McpResourcesResp{ServerID: id, Resources: []handler.McpResourceResp{{URI: "file:///tmp/a.txt", MimeType: "text/plain"}}}, nil
}
func (s *stubSvc) ReadMcpResource(_ context.Context, id, uri string) (*handler.McpResourceReadResp, error) {
	s.mcpLiveID, s.mcpLiveURI = id, uri
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &handler.McpResourceReadResp{ServerID: id, URI: uri, Contents: []handler.McpResourceContent{{URI: uri, Text: "hi"}}}, nil
}
func (s *stubSvc) ListMcpPrompts(_ context.Context, id string) (*handler.McpPromptsResp, error) {
	s.mcpLiveID = id
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &handler.McpPromptsResp{ServerID: id, Prompts: []handler.McpPromptResp{{Name: "greet"}}}, nil
}
func (s *stubSvc) GetMcpPrompt(_ context.Context, id, name string, req handler.McpPromptGetReq) (*handler.McpPromptGetResp, error) {
	s.mcpLiveID, s.mcpLiveName, s.mcpLiveArgs = id, name, req.Arguments
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &handler.McpPromptGetResp{ServerID: id, Name: name, Messages: []handler.McpPromptMessage{{Role: "user", Content: json.RawMessage(`{"type":"text","text":"hi"}`)}}}, nil
}
func (s *stubSvc) ProbeMcpServer(_ context.Context, id string) (*handler.McpProbeResp, error) {
	s.probeID = id
	if s.probeErr != nil {
		return nil, s.probeErr
	}
	if s.probeResp != nil {
		return s.probeResp, nil
	}
	return &handler.McpProbeResp{ID: id, ProbedAt: "2026-09-09T00:00:00Z"}, nil
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

// ——— A3 agent 诊断桩 ———
func (s *stubSvc) AgentDiagnostics(_ context.Context, id string) (*handler.AgentDiagnosticsResp, error) {
	s.diagID = id
	if s.diagErr != nil {
		return nil, s.diagErr
	}
	if s.diagResp != nil {
		return s.diagResp, nil
	}
	return &handler.AgentDiagnosticsResp{AgentID: id, State: "stopped", StderrSource: "none",
		StderrTail: []string{}, CheckedAt: "2026-09-09T00:00:00Z"}, nil
}

// ——— M3 ② 消息检索 / 批量导出桩 ———
func (s *stubSvc) SearchMessages(_ context.Context, q string, limit int) (*handler.MessageSearchResp, error) {
	s.searchMsgQuery, s.searchMsgLimit = q, limit
	if s.searchMsgErr != nil {
		return nil, s.searchMsgErr
	}
	if s.searchMsgResp != nil {
		return s.searchMsgResp, nil
	}
	return &handler.MessageSearchResp{Query: q, Source: "sqlite", Hits: []handler.MessageSearchHit{}}, nil
}

func (s *stubSvc) ExportConversations(_ context.Context, req handler.ExportReq) (*handler.ExportResp, error) {
	s.exportReq = req
	if s.exportErr != nil {
		return nil, s.exportErr
	}
	if s.exportResp != nil {
		return s.exportResp, nil
	}
	return &handler.ExportResp{Format: "markdown", Files: []handler.ExportFile{}}, nil
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
func (s *stubSvc) ResolvePermission(_ context.Context, id, callID, optionID string, always, allowed bool) (*handler.PermissionDecisionResp, error) {
	if s.resolvePermErr != nil {
		return nil, *s.resolvePermErr
	}
	return &handler.PermissionDecisionResp{
		CallID: callID, Allowed: allowed, Rejected: !allowed, Always: always,
		OptionID: optionID, Outcome: "selected", SelectedOptionID: optionID,
	}, nil
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
func (s *stubSvc) ConversationHistory(_ context.Context, id string, q handler.HistoryQuery) (*handler.ConversationHistoryResp, error) {
	s.historyCalled = true
	s.historyID = id
	s.historyAfter = q.After
	s.historyBefore = q.Before
	s.historyHasBefore = q.HasBefore
	s.historyLimit = q.Limit
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
	g.POST("/agents/:id/default", h.Agent.SetDefault)
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
	g.POST("/mcps/:id/probe", h.Capability.ProbeMcpServer)
	g.POST("/mcps/:id/tools/:tool/call", h.Capability.CallMcpTool)
	g.GET("/mcps/:id/resources", h.Capability.ListMcpResources)
	g.POST("/mcps/:id/resources/read", h.Capability.ReadMcpResource)
	g.GET("/mcps/:id/prompts", h.Capability.ListMcpPrompts)
	g.POST("/mcps/:id/prompts/:name/get", h.Capability.GetMcpPrompt)
	g.GET("/tools", h.Capability.ListTools)
	g.GET("/usage/summary", h.Usage.Summary)
	g.GET("/agents/:id/diagnostics", h.Agent.Diagnostics)
	g.GET("/search/messages", h.Search.Messages)
	g.POST("/conversations/export", h.Export.Conversations)
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
		agents: []handler.AgentResp{{ID: "dsh", Name: "dsh", State: "stopped", IsDefault: true, Seed: true}},
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
	if err := json.Unmarshal(env.Data, &agents); err != nil || len(agents) != 1 || agents[0].ID != "dsh" {
		t.Fatalf("agents data 不符: %s", env.Data)
	}
	// 种子只读标记必须落在 JSON 契约里（前端按 a.seed 判定，而非 id 前缀猜测）。
	if !agents[0].Seed || !agents[0].IsDefault {
		t.Fatalf("seed/is_default 未暴露: %+v", agents[0])
	}
	if !strings.Contains(string(env.Data), `"seed":true`) {
		t.Fatalf("seed 字段名不符: %s", env.Data)
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

// TestAgentPermissionPolicyRoute M2 #7：GET /agents 暴露只读 permission_policy；
// PATCH /agents/:id 透传策略入参（handler 只做绑定/透传，校验在 manager）。
func TestAgentPermissionPolicyRoute(t *testing.T) {
	svc := &stubSvc{agents: []handler.AgentResp{{
		ID: "pol", Name: "策略", State: "stopped",
		PermissionPolicy: &handler.PermissionPolicyResp{Default: "deny", Tools: map[string]string{"bash": "allow"}},
	}}}
	r := newRestEngine(svc)

	env, _ := do(t, r, "GET", "/v1/forge/agents", "")
	var agents []handler.AgentResp
	if err := json.Unmarshal(env.Data, &agents); err != nil || len(agents) != 1 {
		t.Fatalf("agents data 不符: %s", env.Data)
	}
	if agents[0].PermissionPolicy == nil || agents[0].PermissionPolicy.Default != "deny" ||
		agents[0].PermissionPolicy.Tools["bash"] != "allow" {
		t.Fatalf("permission_policy 未透传: %+v", agents[0])
	}

	svc.updateAgentResp = &handler.AgentResp{ID: "pol", Name: "策略", State: "stopped",
		PermissionPolicy: &handler.PermissionPolicyResp{Default: "allow"}}
	env, code := do(t, r, "PATCH", "/v1/forge/agents/pol", `{"permission_policy":{"default":"allow"}}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH 失败: http=%d env=%+v", code, env)
	}
	if svc.updateAgentReq.PermissionPolicy == nil || svc.updateAgentReq.PermissionPolicy.Default != "allow" {
		t.Fatalf("策略未透传到 service: %+v", svc.updateAgentReq)
	}
	// 非法策略由 manager 返回 request 类错误 → 信封透传（HTTP 200 + code≠0）。
	svc.updateAgentErr = gerror.Error{Code: 40001, Msg: `permission_policy.default: 动作 "sometimes" 非法`}
	if env, code := do(t, r, "PATCH", "/v1/forge/agents/pol", `{"permission_policy":{"default":"sometimes"}}`); code != 200 || env.Code != 40001 {
		t.Fatalf("非法策略应 40001，got http=%d code=%d", code, env.Code)
	}
}

// TestUsageSummaryRoute M3：GET /usage/summary 参数绑定/透传 + 信封形态 + 业务错误透传。
func TestUsageSummaryRoute(t *testing.T) {
	svc := &stubSvc{usageResp: &handler.UsageSummaryResp{
		From: "2026-09-01T00:00:00Z", To: "2026-09-09T00:00:00Z", AgentID: "agent-echo",
		GroupBy: "day", Source: "sqlite",
		Totals:  handler.UsageBucketResp{Key: "total", Runs: 3, Completed: 2, Failed: 1, DurationMS: 1200},
		Buckets: []handler.UsageBucketResp{{Key: "2026-09-08", Runs: 3}},
	}}
	r := newRestEngine(svc)

	env, code := do(t, r, "GET", "/v1/forge/usage/summary?from=2026-09-01&to=2026-09-09&agent_id=agent-echo&group_by=day", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	if svc.usageQuery.From != "2026-09-01" || svc.usageQuery.To != "2026-09-09" ||
		svc.usageQuery.AgentID != "agent-echo" || svc.usageQuery.GroupBy != "day" {
		t.Fatalf("查询参数未透传: %+v", svc.usageQuery)
	}
	var resp handler.UsageSummaryResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if resp.GroupBy != "day" || resp.Source != "sqlite" || resp.Totals.Runs != 3 ||
		len(resp.Buckets) != 1 || resp.Buckets[0].Key != "2026-09-08" {
		t.Fatalf("响应载荷不符: %+v", resp)
	}

	// 业务错误（40001/40501）→ HTTP 200 + code≠0 透传。
	svc.usageErr = gerror.Error{Code: 40001, Msg: `[invalid_usage_group_by] group_by 仅支持 agent|day："tool"`}
	if env, code := do(t, r, "GET", "/v1/forge/usage/summary?group_by=tool", ""); code != 200 || env.Code != 40001 {
		t.Fatalf("非法 group_by 应 40001: http=%d env=%+v", code, env)
	}
	svc.usageErr = gerror.Error{Code: 40501, Msg: "[usage_stats_unsupported] 内存模式无统计"}
	if env, _ := do(t, r, "GET", "/v1/forge/usage/summary", ""); env.Code != 40501 {
		t.Fatalf("内存模式应 40501: %+v", env)
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
	if !svc.historyCalled || svc.historyID != "s1" || svc.historyAfter != 0 || svc.historyBefore != 0 || svc.historyLimit != 200 {
		t.Fatalf("默认参数不符: %+v %d/%d %d", svc.historyID, svc.historyAfter, svc.historyBefore, svc.historyLimit)
	}

	// 显式 after/limit。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?after=7&limit=50", "")
	if svc.historyAfter != 7 || svc.historyLimit != 50 {
		t.Fatalf("显式参数不符: after=%d limit=%d", svc.historyAfter, svc.historyLimit)
	}

	// before 游标（C2：向前翻页）；after/before 同时给 → 40001（互斥）。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?before=42&limit=5", "")
	if svc.historyBefore != 42 || svc.historyAfter != 0 || svc.historyLimit != 5 || !svc.historyHasBefore {
		t.Fatalf("before 参数不符: before=%d after=%d limit=%d has=%v", svc.historyBefore, svc.historyAfter, svc.historyLimit, svc.historyHasBefore)
	}
	// before=0 显式给出 = 取末尾一页（HasBefore 区分「未给 before」与「before=0」）。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/forge/conversations/s1/history?before=0", "")
	if !svc.historyHasBefore || svc.historyBefore != 0 {
		t.Fatalf("before=0 应置 HasBefore: %+v", svc)
	}
	svc.historyCalled = false
	envBoth, _ := do(t, r, "GET", "/v1/forge/conversations/s1/history?after=1&before=2", "")
	if envBoth.Code != 40001 {
		t.Fatalf("after+before 期望 40001，got %+v", envBoth)
	}
	envBadBefore, _ := do(t, r, "GET", "/v1/forge/conversations/s1/history?before=-3", "")
	if envBadBefore.Code != 40000 {
		t.Fatalf("负 before 期望 40000，got %+v", envBadBefore)
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
	// 切换「当前 Agent」：路径参数透传到服务并回带 is_default 表示。
	env, code = do(t, r, "POST", "/v1/forge/agents/alias-echo/default", "")
	if code != 200 || env.Code != 0 || svc.agentAction != "default:alias-echo" {
		t.Fatalf("set default 失败 code=%d env=%d action=%s", code, env.Code, svc.agentAction)
	}
	var def handler.AgentResp
	if err := json.Unmarshal(env.Data, &def); err != nil || def.ID != "alias-echo" || !def.IsDefault {
		t.Fatalf("set default 载荷不符: %s", env.Data)
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

// TestProbeMcpServerRoute P2 §5.2 MCP live 探测路由：POST /mcps/:id/probe 返回探测结果；
// 未知 server 透传 40401；未知传输透传 40501。
func TestProbeMcpServerRoute(t *testing.T) {
	svc := &stubSvc{
		probeResp: &handler.McpProbeResp{
			ID: "fs", Transport: "stdio", ServerName: "helper", ProbedAt: "2026-09-09T10:00:00Z",
			Added: 2, Removed: 1, ToolCount: 2,
			Tools: []handler.ToolResp{{Name: "read_file", Source: "mcp", McpServer: "fs"}},
		},
	}
	r := newRestEngine(svc)

	env, code := do(t, r, "POST", "/v1/forge/mcps/fs/probe", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST probe 失败 code=%d env=%d", code, env.Code)
	}
	var got handler.McpProbeResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v (%s)", err, env.Data)
	}
	if got.ID != "fs" || svc.probeID != "fs" || got.Added != 2 || got.ToolCount != 2 {
		t.Fatalf("探测结果不符: %+v stub=%q", got, svc.probeID)
	}
	if len(got.Tools) != 1 || got.Tools[0].McpServer != "fs" {
		t.Fatalf("tools 不符: %+v", got.Tools)
	}

	svc.probeErr = gerror.Error{Code: 40401, Msg: `未知 MCP server "nope"`}
	if env, code := do(t, r, "POST", "/v1/forge/mcps/nope/probe", ""); code != 200 || env.Code != 40401 {
		t.Fatalf("未知 server 应 40401，got http=%d code=%d", code, env.Code)
	}
	svc.probeErr = gerror.Error{Code: 40501, Msg: "MCP 未知传输（不臆造）"}
	if env, code := do(t, r, "POST", "/v1/forge/mcps/weird1/probe", ""); code != 200 || env.Code != 40501 {
		t.Fatalf("未知传输应 40501，got http=%d code=%d", code, env.Code)
	}
}

// TestAgentErrorEnvelopeMapping agent 端点错误必须经信封映射（修复：此前裸 acl 错误漏成 code=-1，
// 与 README「非 0 为业务错误（40001/40401/…）」契约不符）。
func TestAgentErrorEnvelopeMapping(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		code   int
	}{
		{"delete_seed", "DELETE", "/v1/forge/agents/dsh", "",
			acl.NewError(acl.ClassRequest, "seed_agent_readonly", "种子 agent 不可删除", nil), 40001},
		{"delete_unknown", "DELETE", "/v1/forge/agents/nope", "",
			acl.NewError(acl.ClassRequest, "agent_not_found", "未知 agent", nil), 40401},
		{"update_seed_runtime", "PATCH", "/v1/forge/agents/dsh", `{"runtimes":["echo"]}`,
			acl.NewError(acl.ClassRequest, "seed_agent_readonly", "种子 runtime 只读", nil), 40001},
		{"create_reserved", "POST", "/v1/forge/agents", `{"id":"agent-x","runtimes":["echo"]}`,
			acl.NewError(acl.ClassRequest, "reserved_agent_id", "保留前缀", nil), 40001},
		{"start_unknown", "POST", "/v1/forge/agents/nope/start", "",
			acl.NewError(acl.ClassRequest, "agent_not_found", "未知 agent", nil), 40401},
		{"stop_busy", "POST", "/v1/forge/agents/dsh/stop", "",
			acl.NewError(acl.ClassLimit, "agent_busy", "有在途 turn", nil), 40901},
		{"start_unavailable", "POST", "/v1/forge/agents/dsh/start", "",
			acl.NewError(acl.ClassUnsupported, "runtime_unavailable", "命令不可执行", nil), 40501},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubSvc{agentErr: tc.err}
			r := newRestEngine(svc)
			env, http := do(t, r, tc.method, tc.path, tc.body)
			if http != 200 || env.Code != tc.code {
				t.Fatalf("期望 HTTP 200 code=%d，got http=%d code=%d msg=%q", tc.code, http, env.Code, env.Msg)
			}
			if env.Msg == "" || strings.HasPrefix(env.Msg, "acl error") {
				t.Fatalf("msg 应为可读详情，got %q", env.Msg)
			}
		})
	}
	// 已映射错误不得被二次映射（restErr 幂等）。
	svc := &stubSvc{agentErr: gerror.Error{Code: 40001, Msg: "已映射"}}
	r := newRestEngine(svc)
	env, _ := do(t, r, "DELETE", "/v1/forge/agents/nope", "")
	if env.Code != 40001 || env.Msg != "已映射" {
		t.Fatalf("已映射错误应透传: %+v", env)
	}
}

// TestMessageSearchRoute GET /search/messages：参数校验（q 必填、limit 正整数）、
// 查询透传与信封形状（M3 ② 锚点跳转数据面）。
func TestMessageSearchRoute(t *testing.T) {
	svc := &stubSvc{searchMsgResp: &handler.MessageSearchResp{
		Query: "502", Source: "sqlite", Total: 1, HasMore: false,
		Hits: []handler.MessageSearchHit{{ConversationID: "c-1", Title: "部署", Role: "user", Seq: 7, Snippet: "网关 502"}},
	}}
	r := newRestEngine(svc)

	env, http := do(t, r, "GET", "/v1/forge/search/messages?q=502&limit=5", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	if svc.searchMsgQuery != "502" || svc.searchMsgLimit != 5 {
		t.Fatalf("参数透传不符: q=%q limit=%d", svc.searchMsgQuery, svc.searchMsgLimit)
	}
	if !strings.Contains(string(env.Data), `"seq":7`) || !strings.Contains(string(env.Data), `"source":"sqlite"`) {
		t.Fatalf("响应载荷不符: %s", env.Data)
	}

	// 缺 q → 40001（HTTP 200 + 信封非 0）。
	env, http = do(t, r, "GET", "/v1/forge/search/messages", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_query") {
		t.Fatalf("缺 q 校验不符: http=%d %+v", http, env)
	}
	// 非法 limit → 40001。
	env, http = do(t, r, "GET", "/v1/forge/search/messages?q=a&limit=abc", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_limit") {
		t.Fatalf("limit 校验不符: http=%d %+v", http, env)
	}
	// 默认 limit=30。
	_, _ = do(t, r, "GET", "/v1/forge/search/messages?q=a", "")
	if svc.searchMsgLimit != 30 {
		t.Fatalf("默认 limit 应为 30: %d", svc.searchMsgLimit)
	}
}

// TestExportRoute POST /conversations/export：请求体校验 + 响应形状 + 错误映射。
func TestExportRoute(t *testing.T) {
	svc := &stubSvc{exportResp: &handler.ExportResp{
		GeneratedAt: "2026-09-09T00:00:00Z", Format: "markdown", Exported: 1, TotalBytes: 12,
		Files: []handler.ExportFile{{ConversationID: "c-1", Filename: "部署-c-1.md", Format: "markdown", Bytes: 12, Content: "# 部署\n\nhi"}},
	}}
	r := newRestEngine(svc)

	env, http := do(t, r, "POST", "/v1/forge/conversations/export", `{"ids":["c-1"],"format":"markdown"}`)
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	if svc.exportReq.Format != "markdown" || len(svc.exportReq.IDs) != 1 {
		t.Fatalf("请求透传不符: %+v", svc.exportReq)
	}
	if !strings.Contains(string(env.Data), `"filename":"部署-c-1.md"`) || !strings.Contains(string(env.Data), `"total_bytes":12`) {
		t.Fatalf("响应载荷不符: %s", env.Data)
	}

	// 缺 ids → 40000（参数绑定失败）。
	env, http = do(t, r, "POST", "/v1/forge/conversations/export", `{"format":"text"}`)
	if http != 200 || env.Code != 40000 {
		t.Fatalf("缺 ids 校验不符: http=%d %+v", http, env)
	}
	// 服务错误映射（40001 非法格式）。
	svc2 := &stubSvc{exportErr: gerror.Error{Code: 40001, Msg: "不支持的导出格式 [invalid_export_format]"}}
	r2 := newRestEngine(svc2)
	env, http = do(t, r2, "POST", "/v1/forge/conversations/export", `{"ids":["c-1"],"format":"pdf"}`)
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_export_format") {
		t.Fatalf("错误映射不符: http=%d %+v", http, env)
	}
}

// TestAgentDiagnosticsRoute GET /agents/:id/diagnostics：载荷形状（stderr 尾巴 + 最近失败）+ 错误映射。
func TestAgentDiagnosticsRoute(t *testing.T) {
	svc := &stubSvc{diagResp: &handler.AgentDiagnosticsResp{
		AgentID: "a1", Name: "A1", State: "stopped", Runtime: "echo", Command: "sh",
		Connected: false, StderrSource: "last_error", CheckedAt: "2026-09-09T00:00:00Z",
		StderrTail: []string{"[warn] boom"},
		LastError:  &handler.AgentLastErrorResp{At: "2026-09-09T00:00:00Z", Code: "timeout", Class: "connection", Details: "握手超时"},
	}}
	r := newRestEngine(svc)

	env, http := do(t, r, "GET", "/v1/forge/agents/a1/diagnostics", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	if svc.diagID != "a1" {
		t.Fatalf("id 透传不符: %q", svc.diagID)
	}
	body := string(env.Data)
	for _, want := range []string{`"stderr_source":"last_error"`, `"[warn] boom"`, `"code":"timeout"`, `"connected":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺 %s: %s", want, body)
		}
	}

	// 未知 agent → 40401（handler 经 svc.RestErr 映射）。
	svc2 := &stubSvc{diagErr: acl.NewError(acl.ClassRequest, "agent_not_found", "未知 agent", nil)}
	r2 := newRestEngine(svc2)
	env, http = do(t, r2, "GET", "/v1/forge/agents/nope/diagnostics", "")
	if http != 200 || env.Code != 40401 || !strings.Contains(env.Msg, "未知 agent") {
		t.Fatalf("错误映射不符: http=%d %+v", http, env)
	}
}

// TestCallMcpToolRoute B1 MCP 工具调用路由：参数透传（server/tool/arguments/timeout_ms）、
// 工具级失败（is_error=true）仍是 code=0，传输/协议错误透传信封码。
func TestCallMcpToolRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	body := `{"arguments":{"path":"/tmp/a"},"timeout_ms":1500}`
	env, code := do(t, r, "POST", "/v1/forge/mcps/fs/tools/read_file/call", body)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST call 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	if svc.callMcpID != "fs" || svc.callMcpTool != "read_file" {
		t.Fatalf("路径参数未透传: id=%q tool=%q", svc.callMcpID, svc.callMcpTool)
	}
	if string(svc.callMcpReq.Arguments) != `{"path":"/tmp/a"}` || svc.callMcpReq.TimeoutMS != 1500 {
		t.Fatalf("请求体未透传: %+v", svc.callMcpReq)
	}
	var got handler.McpToolCallResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v", err)
	}
	if got.Text != "ok" || len(got.Content) != 1 {
		t.Fatalf("结果不符: %+v", got)
	}

	// 空 body = 无参数调用（不应报参数错误）。
	svc.callMcpReq = handler.McpToolCallReq{}
	if env, code := do(t, r, "POST", "/v1/forge/mcps/fs/tools/t/call", ""); code != 200 || env.Code != 0 {
		t.Fatalf("空 body 应放行，got http=%d code=%d", code, env.Code)
	}
	if len(svc.callMcpReq.Arguments) != 0 || svc.callMcpReq.TimeoutMS != 0 {
		t.Fatalf("空 body 应零值: %+v", svc.callMcpReq)
	}

	// 非法 JSON body → 40000（参数错误）。
	if env, code := do(t, r, "POST", "/v1/forge/mcps/fs/tools/t/call", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}

	// 工具级失败：仍 code=0，is_error 表达。
	svc.callMcpResp = &handler.McpToolCallResp{
		ServerID: "fs", Tool: "read_file", IsError: true,
		Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"权限不足"}`)}, Text: "权限不足",
	}
	env, _ = do(t, r, "POST", "/v1/forge/mcps/fs/tools/read_file/call", "")
	var failed handler.McpToolCallResp
	_ = json.Unmarshal(env.Data, &failed)
	if env.Code != 0 || !failed.IsError {
		t.Fatalf("工具级失败应 code=0 + is_error=true，got code=%d %+v", env.Code, failed)
	}

	// 未知 server → 40401；传输不支持 → 40501；超时 → 40901。
	svc.callMcpResp = nil
	for _, tc := range []struct {
		err  error
		want int
	}{
		{gerror.Error{Code: 40401, Msg: `未知 MCP server "nope"`}, 40401},
		{gerror.Error{Code: 40501, Msg: "MCP sse 传输暂不支持"}, 40501},
		{gerror.Error{Code: 40901, Msg: "[mcp_timeout] tools/call 失败"}, 40901},
	} {
		svc.callMcpErr = tc.err
		if env, code := do(t, r, "POST", "/v1/forge/mcps/x/tools/t/call", ""); code != 200 || env.Code != tc.want {
			t.Fatalf("错误码透传不符 want=%d got http=%d code=%d", tc.want, code, env.Code)
		}
	}
}

// TestMcpResourcesPromptsRoutes B2：资源/提示词四条路由（路径与 body 参数透传、错误码透传）。
func TestMcpResourcesPromptsRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)

	env, code := do(t, r, "GET", "/v1/forge/mcps/fs/resources", "")
	if code != 200 || env.Code != 0 || svc.mcpLiveID != "fs" {
		t.Fatalf("GET resources 失败 http=%d code=%d id=%q", code, env.Code, svc.mcpLiveID)
	}
	var res handler.McpResourcesResp
	_ = json.Unmarshal(env.Data, &res)
	if len(res.Resources) != 1 || res.Resources[0].URI != "file:///tmp/a.txt" {
		t.Fatalf("资源结果不符: %+v", res)
	}

	env, code = do(t, r, "POST", "/v1/forge/mcps/fs/resources/read", `{"uri":"file:///tmp/a.txt"}`)
	if code != 200 || env.Code != 0 || svc.mcpLiveURI != "file:///tmp/a.txt" {
		t.Fatalf("POST resources/read 失败 http=%d code=%d uri=%q", code, env.Code, svc.mcpLiveURI)
	}
	var rd handler.McpResourceReadResp
	_ = json.Unmarshal(env.Data, &rd)
	if len(rd.Contents) != 1 || rd.Contents[0].Text != "hi" {
		t.Fatalf("读取结果不符: %+v", rd)
	}
	// 缺 uri → 桩返回空（真实实现报 40001）；非法 JSON → 40000。
	if env, code := do(t, r, "POST", "/v1/forge/mcps/fs/resources/read", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}

	env, code = do(t, r, "GET", "/v1/forge/mcps/fs/prompts", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET prompts 失败 http=%d code=%d", code, env.Code)
	}
	var ps handler.McpPromptsResp
	_ = json.Unmarshal(env.Data, &ps)
	if len(ps.Prompts) != 1 || ps.Prompts[0].Name != "greet" {
		t.Fatalf("提示词列表不符: %+v", ps)
	}

	env, code = do(t, r, "POST", "/v1/forge/mcps/fs/prompts/greet/get", `{"arguments":{"who":"世界"}}`)
	if code != 200 || env.Code != 0 || svc.mcpLiveName != "greet" || svc.mcpLiveArgs["who"] != "世界" {
		t.Fatalf("POST prompts/get 失败 http=%d code=%d name=%q args=%v", code, env.Code, svc.mcpLiveName, svc.mcpLiveArgs)
	}
	var gp handler.McpPromptGetResp
	_ = json.Unmarshal(env.Data, &gp)
	if len(gp.Messages) != 1 || gp.Messages[0].Role != "user" {
		t.Fatalf("提示词结果不符: %+v", gp)
	}
	// 空 body = 无参数取用。
	svc.mcpLiveArgs = map[string]string{"x": "y"}
	if env, code := do(t, r, "POST", "/v1/forge/mcps/fs/prompts/greet/get", ""); code != 200 || env.Code != 0 || len(svc.mcpLiveArgs) != 0 {
		t.Fatalf("空 body 应零值，got http=%d code=%d args=%v", code, env.Code, svc.mcpLiveArgs)
	}

	// 错误码透传：40401 / 40501 / 50201。
	for _, tc := range []struct {
		err  error
		want int
		path string
	}{
		{gerror.Error{Code: 40401, Msg: "未知 MCP server"}, 40401, "/v1/forge/mcps/nope/resources"},
		{gerror.Error{Code: 40501, Msg: "未知传输"}, 40501, "/v1/forge/mcps/s/prompts"},
		{gerror.Error{Code: 50201, Msg: "[mcp_resource_read_failed] x"}, 50201, "/v1/forge/mcps/fs/resources"},
	} {
		svc.mcpLiveErr = tc.err
		if env, code := do(t, r, "GET", tc.path, ""); code != 200 || env.Code != tc.want {
			t.Fatalf("错误码透传不符 want=%d got http=%d code=%d", tc.want, code, env.Code)
		}
	}
}

// TestResolvePermissionReceiptShape C5：POST /conversations/:id/permission 回执描述
// **实际投递结果**（allowed/rejected/always/请求 option_id/outcome/selected_option_id），
// 不再是 `{call_id, allowed}` 的请求参数回显。
func TestResolvePermissionReceiptShape(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(svc)
	env, code := do(t, r, "POST", "/v1/forge/conversations/s1/permission",
		`{"call_id":"tc1","option_id":"allow_always","always":true,"allowed":true}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("决策信封不符: code=%d env=%+v", code, env)
	}
	var resp handler.PermissionDecisionResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("回执解析失败: %v data=%s", err, env.Data)
	}
	if resp.CallID != "tc1" || !resp.Allowed || resp.Rejected || !resp.Always ||
		resp.OptionID != "allow_always" || resp.Outcome != "selected" || resp.SelectedOptionID != "allow_always" {
		t.Fatalf("回执字段不符: %+v", resp)
	}
	for _, key := range []string{`"rejected"`, `"always"`, `"outcome"`, `"selected_option_id"`} {
		if !strings.Contains(string(env.Data), key) {
			t.Fatalf("回执缺字段 %s: %s", key, env.Data)
		}
	}
	// 拒绝决策：rejected=true、allowed=false（不再出现「allowed:false 但语义是拒绝」的含糊回执）。
	env, _ = do(t, r, "POST", "/v1/forge/conversations/s1/permission",
		`{"call_id":"tc2","option_id":"reject_once","allowed":false}`)
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("拒绝回执解析失败: %v", err)
	}
	if resp.Allowed || !resp.Rejected || resp.OptionID != "reject_once" {
		t.Fatalf("拒绝回执字段不符: %+v", resp)
	}
}
