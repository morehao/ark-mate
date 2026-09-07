package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/gerror"
)

// AgentRec 是进程内 Agent 定义（M0 为每个已注册 runtime 播种一个默认 Agent）。
type AgentRec struct {
	ID      string
	Name    string
	Runtime string
	State   string
}

// SessionRec 是进程内会话终态记录（M1 内存态；M2 落 SQLite 后由 events 表回放）。
type SessionRec struct {
	ID        string
	AgentID   string
	Runtime   string
	Status    string
	Title     string // 会话标题（首条用户消息截断生成；可被 PATCH 覆盖）
	Pinned    bool   // 置顶（侧栏/管理面用）
	Archived  bool   // 归档（软删除；会话列表默认不展示）
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表；agent 为每 runtime 的默认播种；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	reg  *runtime.Registry
	bus  *eventbus.Bus
	orch *Orchestrator
	tx   *transcriptRecorder // 会话 wire transcript（nil = 未启用，见 SetTranscriptDir）

	mu       sync.Mutex
	agents   map[string]*AgentRec
	sessions map[string]*SessionRec
	ver      map[string]int64 // 每 agg(session) 的版本计数（域事件递增）
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
func NewService(reg *runtime.Registry, bus *eventbus.Bus) *Service {
	s := &Service{
		reg:      reg,
		bus:      bus,
		agents:   make(map[string]*AgentRec),
		sessions: make(map[string]*SessionRec),
		ver:      make(map[string]int64),
	}
	// 每 runtime 播种一个默认 Agent（M0 简化：无 Agent CRUD，见 roadmap M2）。
	for _, spec := range reg.List() {
		id := "agent-" + spec.ID
		s.agents[id] = &AgentRec{
			ID:      id,
			Name:    spec.Name + "-agent",
			Runtime: spec.ID,
			State:   AgentStateStopped,
		}
	}
	return s
}

// SetBus 注入事件总线（Orchestrator 构造时调用；与 handler.New 共用同一 bus）。
func (s *Service) SetBus(bus *eventbus.Bus) { s.bus = bus }

// SetTranscriptDir 启用会话 wire transcript 落盘（目录不存在会自动创建；
// dir 为空 = 禁用）。须在任何会话开始前调用（serve/demo 装配时）。
func (s *Service) SetTranscriptDir(dir string) {
	if dir == "" {
		s.tx = nil
		return
	}
	s.tx = newTranscriptRecorder(dir)
}

// beginTranscript 为会话写 transcript header（首行）；失败只告警不阻断。
func (s *Service) beginTranscript(sessionID, agentID, runtimeID, cwd string) {
	if s.tx == nil {
		return
	}
	s.tx.begin(sessionID, agentID, runtimeID, cwd)
}

// closeTranscripts 关闭全部 transcript 文件（daemon 退出/orchestrator.Close 收尾）。
func (s *Service) closeTranscripts() {
	if s.tx == nil {
		return
	}
	s.tx.closeAll()
	s.tx = nil
}

// ListAgents 返回播种的全部 Agent。
func (s *Service) ListAgents() ([]handler.AgentResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]handler.AgentResp, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, handler.AgentResp{ID: a.ID, Name: a.Name, Runtime: a.Runtime, State: a.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListRuntimes 返回注册表全部 runtime（含静态能力基线）。
func (s *Service) ListRuntimes() ([]handler.RuntimeResp, error) {
	specs := s.reg.List()
	out := make([]handler.RuntimeResp, 0, len(specs))
	for _, sp := range specs {
		out = append(out, handler.RuntimeResp{
			Name: sp.ID,
			ID:   sp.ID,
			Vers: sp.Version,
			Caps: capabilityKVs(sp.Caps),
		})
	}
	return out, nil
}

// capabilityKVs 把 runtime.Capabilities 展开为 key/value 列表（稳定序）。
func capabilityKVs(c runtime.Capabilities) []handler.CapabilityKV {
	return []handler.CapabilityKV{
		{Key: "sessions_persistent", Value: c.SessionsPersistent},
		{Key: "multi_session_concurrent", Value: c.MultiSessionConcurrent},
		{Key: "images", Value: c.Images},
		{Key: "embedded_context", Value: c.EmbeddedContext},
	}
}

// ListSessions 返回会话概要；archived=false 仅未归档（默认），true 仅归档。
// 按创建时间倒序（置顶优先）。
func (s *Service) ListSessions(archived bool) ([]handler.SessionResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]handler.SessionResp, 0, len(s.sessions))
	for _, r := range s.sessions {
		if r.Archived != archived {
			continue
		}
		out = append(out, toSessionResp(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// toSessionResp 把 SessionRec 折叠为 handler.SessionResp。
func toSessionResp(r *SessionRec) handler.SessionResp {
	return handler.SessionResp{
		ID:        r.ID,
		AgentID:   r.AgentID,
		Runtime:   r.Runtime,
		Status:    r.Status,
		Title:     r.Title,
		Pinned:    r.Pinned,
		Archived:  r.Archived,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// GetSession 返回单会话详情（元数据 + 挂起权限）。
func (s *Service) GetSession(ctx context.Context, id string) (*handler.SessionDetailResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	perms := make([]handler.PermissionResp, 0, len(pending))
	for _, p := range pending {
		perms = append(perms, toPermissionResp(p))
	}
	detail := handler.SessionDetailResp{SessionResp: s.sessionResp(id), Pending: perms}
	return &detail, nil
}

// UpdateSession 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
func (s *Service) UpdateSession(ctx context.Context, id string, patch handler.SessionPatch) (*handler.SessionResp, error) {
	s.mu.Lock()
	r, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if patch.Title != nil {
		r.Title = *patch.Title
	}
	if patch.Pinned != nil {
		r.Pinned = *patch.Pinned
	}
	if patch.Archived != nil {
		r.Archived = *patch.Archived
	}
	r.UpdatedAt = time.Now().UTC()
	resp := toSessionResp(r)
	s.mu.Unlock()
	return &resp, nil
}

// sessionResp 返回单个会话的轻量表示（须持锁调用）。
func (s *Service) sessionResp(id string) handler.SessionResp {
	r := s.sessions[id]
	return toSessionResp(r)
}

// ListPendingPermissions 列出某会话的挂起权限请求。
func (s *Service) ListPendingPermissions(ctx context.Context, id string) ([]handler.PermissionResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	out := make([]handler.PermissionResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toPermissionResp(p))
	}
	return out, nil
}

// ResolvePermission 按 call_id 决策（allow/reject × once/always）。
func (s *Service) ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) error {
	if s.orch == nil {
		return s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	ok, err := s.orch.ResolvePermission(callID, optionID, always, allowed)
	if err != nil {
		return s.restErr(err)
	}
	if !ok {
		return s.restErr(acl.NewError(acl.ClassRequest, "permission_not_found", fmt.Sprintf("权限请求 %q 不存在或已过期", callID), nil))
	}
	return nil
}

// PendingPermissions 直接透传编排器的挂起权限（供 GetSession/ListPendingPermissions 复用）。
func (s *Service) PendingPermissions(id string) []PermissionItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingPermissions(id)
}

// toPermissionResp 把 manager.PermissionItem 折叠为 handler.PermissionResp。
func toPermissionResp(p PermissionItem) handler.PermissionResp {
	opts := make([]handler.PermissionOptionResp, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, handler.PermissionOptionResp{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return handler.PermissionResp{
		CallID:    p.CallID,
		SessionID: p.SessionID,
		Title:     p.Title,
		ToolCall:  p.ToolCall.ID,
		Kind:      p.ToolCall.Kind,
		Input:     p.ToolCall.Input,
		Options:   opts,
		TimeoutAt: p.TimeoutAt.UTC().Format(time.RFC3339),
	}
}

// agentForRuntime 返回指定 runtime 的默认播种 Agent。
func (s *Service) agentForRuntime(runtimeID string) (*AgentRec, bool) {
	a, ok := s.agents["agent-"+runtimeID]
	return a, ok
}

// setAgentState 更新 Agent 运行态（Orchestrator 开/关连接时调用）。
func (s *Service) setAgentState(agentID, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[agentID]; ok {
		a.State = state
	}
}

// registerSession 登记一个新会话，返回记录指针。
func (s *Service) registerSession(rec *SessionRec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	s.sessions[rec.ID] = rec
}

// markSession 更新会话终态。
func (s *Service) markSession(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.sessions[id]; ok {
		r.Status = status
		r.UpdatedAt = time.Now().UTC()
	}
}

// publish 把域事件发布进 eventbus（agg = sessionID；每 agg 版本递增），
// 并旁路追加到该会话的 wire transcript（未启用/非 map 载荷时跳过）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (s *Service) publish(typ, sessionID string, payload any) eventbus.Event {
	s.mu.Lock()
	s.ver[sessionID]++
	v := s.ver[sessionID]
	s.mu.Unlock()

	bus := s.bus
	var evt eventbus.Event
	if bus == nil {
		evt = eventbus.Event{Type: typ, AggregID: sessionID, Version: v}
	} else {
		evt = bus.Publish(typ, sessionID, v, payload)
	}
	if m, ok := payload.(map[string]any); ok {
		s.tx.appendLine(sessionID, evt.Seq, typ, m)
		// 会话标题自动推导：首个用户消息作为标题（截断）。PATCH 可覆盖（见 UpdateSession）。
		if typ == BusUserMessage {
			if r, ok := s.sessions[sessionID]; ok && strings.TrimSpace(r.Title) == "" {
				if text, _ := m["text"].(string); text != "" {
					r.Title = deriveTitle(text)
				}
			}
		}
	}
	return evt
}

// requireAgent 供 Orchestrator 用 runtimeID 取播种 Agent。
func (s *Service) requireAgent(runtimeID string) (*AgentRec, error) {
	a, ok := s.agentForRuntime(runtimeID)
	if !ok {
		return nil, fmt.Errorf("runtime %q 无默认 Agent（注册表未含该 runtime）", runtimeID)
	}
	return a, nil
}

// AgentByID 按 ID 取 Agent（会话动作/REST 用）。
func (s *Service) AgentByID(id string) (*AgentRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	return a, ok
}

// attachOrchestrator 由 NewOrchestrator 回挂；Service 的会话动作委托给它。
func (s *Service) attachOrchestrator(o *Orchestrator) { s.orch = o }

// deriveTitle 从首条用户消息推导会话标题（单行、截断、折叠空白）。
func deriveTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 48 {
		runes := []rune(text)
		text = string(runes[:48]) + "…"
	}
	return text
}

// ——— handler.Service 的会话动作实现（REST：create/prompt/cancel） ———

// CreateSession 在 agent 对应 runtime 上建立会话，若携带 prompt 则同步发起首轮
// （后台执行，事件经 bus/WS 出流）。返回的 EventSeq 为「已发布事件」后的水位，
// 前端 WS attach 传 since_seq=该值即可续传首轮增量。
func (s *Service) CreateSession(ctx context.Context, req handler.SessionCreateReq) (*handler.SessionCreateResp, error) {
	if s.orch == nil {
		return nil, s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if req.AgentID == "" {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "agent_required", "agent_id 必填", nil))
	}
	// 会话与 turn 生命周期不随 HTTP 请求结束：WithoutCancel 继承 values 但脱离取消
	//（turn 由 Orchestrator 持有，取消走 POST /sessions/:id/cancel）。
	runCtx := context.WithoutCancel(ctx)
	res, err := s.orch.StartSession(runCtx, req.AgentID, req.Cwd)
	if err != nil {
		return nil, s.restErr(err)
	}
	resp := &handler.SessionCreateResp{
		ID: res.SessionID, AgentID: res.AgentID, Runtime: res.Runtime, Status: res.Status, EventSeq: res.EventSeq,
	}
	if req.Prompt != "" {
		seq, err := s.orch.Prompt(runCtx, res.SessionID, req.Prompt)
		if err != nil {
			return nil, s.restErr(err)
		}
		resp.EventSeq = seq
		resp.Status = SessionStatusInProgress
	}
	return resp, nil
}

// PromptSession 向既有会话追加一轮 prompt（后台执行）。
// turn 生命周期同样脱离 HTTP 请求（WithoutCancel），结束/取消只由 agent 或
// POST /sessions/:id/cancel 决定。
func (s *Service) PromptSession(ctx context.Context, id, prompt string) (*handler.SessionPromptResp, error) {
	if s.orch == nil {
		return nil, s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if id == "" || prompt == "" {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "prompt_required", "session_id 与 prompt 必填", nil))
	}
	seq, err := s.orch.Prompt(context.WithoutCancel(ctx), id, prompt)
	if err != nil {
		return nil, s.restErr(err)
	}
	return &handler.SessionPromptResp{ID: id, Status: SessionStatusInProgress, EventSeq: seq}, nil
}

// CancelTurn 取消会话进行中的 turn（无 turn 时幂等 no-op）。
func (s *Service) CancelTurn(ctx context.Context, id string) error {
	if s.orch == nil {
		return s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if err := s.orch.CancelTurn(ctx, id); err != nil {
		return s.restErr(err)
	}
	return nil
}

// restErr 把领域错误（acl 分类）映射为对外可操作的业务错误码（gerror.Error）。
func (s *Service) restErr(err error) error {
	ae, ok := acl.As(err)
	if !ok {
		return gerror.Error{Code: 50000, Msg: err.Error()}
	}
	code := 50001
	switch ae.Class {
	case acl.ClassRequest:
		code = 40001
		if ae.Code == "agent_not_found" || ae.Code == "session_not_found" || ae.Code == "runtime_not_found" {
			code = 40401
		}
	case acl.ClassLimit:
		code = 40901
	case acl.ClassAccess:
		code = 40101
	case acl.ClassUnsupported:
		code = 40501
	case acl.ClassService, acl.ClassConnection:
		code = 50201
	}
	msg := ae.Details
	if msg == "" {
		msg = ae.Code
	}
	return gerror.Error{Code: code, Msg: msg}
}

// SessionHistory 返回会话事件历史（eventbus 按聚合回放，after/limit 分页，seq 升序）。
// M1 为内存回放：会话须在本进程登记过（进程内即 daemon 会话表），未知会话 → 40401。
func (s *Service) SessionHistory(ctx context.Context, id string, after int64, limit int) (*handler.SessionHistoryResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if after < 0 {
		after = 0
	}
	if limit < 1 {
		limit = 1
	}
	all := s.bus.EventsAfter(after)
	events := make([]eventbus.Event, 0, min(limit, len(all)))
	hasMore := false
	for _, evt := range all {
		if evt.AggregID != id {
			continue
		}
		if len(events) >= limit {
			hasMore = true
			break
		}
		events = append(events, evt)
	}
	return &handler.SessionHistoryResp{ID: id, Events: events, HasMore: hasMore}, nil
}

// hasSession 判断会话是否在本进程登记。
func (s *Service) hasSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}
