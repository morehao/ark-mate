package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/golib/gerror"
)

// AgentRec 是进程内 Agent 定义（M0 为每个已注册 runtime 播种一个默认 Agent）。
type AgentRec struct {
	ID       string
	Name     string
	Runtime  string
	Provider string // provider 档案 id（P2 provider 注入面）；空 = 默认
	State    string
}

// SessionRec 是进程内会话终态记录（内存镜像；sqlite 模式下与 sessions 行同源，
// Cwd 供断点恢复重建 ACP 会话时还原工作目录）。
type SessionRec struct {
	ID        string
	AgentID   string
	Runtime   string
	Cwd       string // 会话工作目录（绝对路径；仅 sqlite 持久化）
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
	st   *store.Store        // SQLite 持久层（nil = 未启用；启用后见 EnableStore）

	agentDir  string                  // Agent definitions 目录（P2；空 = 不落盘，仅内存）
	providers map[string]*providerDef // provider 档案（P2；同一目录 providers.json 持久化）

	mu        sync.Mutex
	agents    map[string]*AgentRec
	sessions  map[string]*SessionRec
	ver       map[string]int64 // 每 agg(session) 的版本计数（域事件递增）
	restoring bool             // RestoreFromStore 进行中：registerSession 跳过 DB 回写
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
func NewService(reg *runtime.Registry, bus *eventbus.Bus) *Service {
	s := &Service{
		reg:       reg,
		bus:       bus,
		agents:    make(map[string]*AgentRec),
		sessions:  make(map[string]*SessionRec),
		ver:       make(map[string]int64),
		providers: make(map[string]*providerDef),
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
// 启用后立即回读已落盘 transcript，重建会话列表与历史（会话重启存活）。
func (s *Service) SetTranscriptDir(dir string) {
	if dir == "" {
		s.tx = nil
		return
	}
	s.tx = newTranscriptRecorder(dir)
	s.loadTranscripts()
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

// GetSession 返回单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
func (s *Service) GetSession(ctx context.Context, id string) (*handler.SessionDetailResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	perms := make([]handler.PermissionResp, 0, len(pending))
	for _, p := range pending {
		perms = append(perms, toPermissionResp(p))
	}
	mems := make([]handler.MemoryResp, 0)
	for _, it := range s.PermissionMemories(id) {
		mems = append(mems, handler.MemoryResp{Title: it.Title, Kind: it.Kind, Decision: it.Decision, Since: it.Since.UTC().Format(time.RFC3339)})
	}
	detail := handler.SessionDetailResp{SessionResp: s.sessionResp(id), Pending: perms, Memories: mems}
	return &detail, nil
}

// UpdateSession 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
// sqlite 模式下同步写库（sessions 为 DB 权威元数据镜像）。
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

	if s.st != nil {
		meta := store.SessionMetaPatch{Title: patch.Title, Pinned: patch.Pinned, Archived: patch.Archived}
		ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateSessionMeta(ctx2, id, meta); err != nil {
			slog.Default().Warn("store: update session meta failed", "session", id, "err", err)
		}
	}
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

// PendingElicitations 直接透传编排器的挂起结构化提问（供 ListPendingElicitations 复用）。
func (s *Service) PendingElicitations(id string) []ElicitationItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingElicitations(id)
}

// ListPendingElicitations 列出某会话的挂起结构化提问。
func (s *Service) ListPendingElicitations(ctx context.Context, id string) ([]handler.ElicitationResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingElicitations(id)
	out := make([]handler.ElicitationResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toElicitationResp(p))
	}
	return out, nil
}

// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel；accept 可带 content）。
func (s *Service) ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error {
	if !s.hasSession(id) {
		return s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	ok, err := s.orch.ResolveElicitation(elicitationID, action, content)
	if err != nil {
		return s.restErr(err)
	}
	if !ok {
		return s.restErr(acl.NewError(acl.ClassRequest, "elicitation_not_found", fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil))
	}
	return nil
}

// PermissionMemories 直接透传编排器的会话级权限记忆（供 GetSession 复用；REST 见 ListPermissionMemories）。
func (s *Service) PermissionMemories(id string) []MemoryItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PermissionMemories(id)
}

// ListPermissionMemories 校验会话存在后透传会话级权限记忆（allow/reject 双侧）。
func (s *Service) ListPermissionMemories(ctx context.Context, id string) ([]handler.MemoryResp, error) {
	if !s.hasSession(id) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	mems := make([]handler.MemoryResp, 0)
	for _, it := range s.PermissionMemories(id) {
		mems = append(mems, handler.MemoryResp{Title: it.Title, Kind: it.Kind, Decision: it.Decision, Since: it.Since.UTC().Format(time.RFC3339)})
	}
	return mems, nil
}

// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
func (s *Service) RevokeMemory(ctx context.Context, id, title, kind string) error {
	if !s.hasSession(id) {
		return s.restErr(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return s.restErr(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if _, err := s.orch.RevokeMemory(id, title, kind); err != nil {
		return s.restErr(err)
	}
	return nil
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

// toElicitationResp 把 manager.ElicitationItem 折叠为 handler.ElicitationResp。
func toElicitationResp(p ElicitationItem) handler.ElicitationResp {
	resp := handler.ElicitationResp{
		ElicitationID: p.ID,
		SessionID:     p.SessionID,
		Mode:          p.Mode,
		Message:       p.Message,
		TimeoutAt:     p.TimeoutAt.UTC().Format(time.RFC3339),
	}
	if p.Mode == "form" {
		resp.Schema = p.Schema
	} else {
		resp.URL = p.URL
	}
	return resp
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
// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的全局 seq 为权威，bus 用 PublishAt 镜像同一号序；
//   - registerSession/markSession/UpdateSession 同步写库（sessions 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
func (s *Service) EnableStore(st *store.Store) { s.st = st }

// RestoreFromStore 启动恢复（sqlite 模式）：从 DB 载入会话记录并把全部已落库事件
// 注入 eventbus（seq 原样 PublishAt）——重启后 bus 与 DB 共用同一全局号序，
// WS/断线续传游标不漂移，旧会话仍可回放。返回恢复的会话数与注入事件数。
func (s *Service) RestoreFromStore(ctx context.Context) (int, int, error) {
	if s.st == nil {
		return 0, 0, nil
	}
	list, err := s.st.ListSessions(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("restore: list sessions: %w", err)
	}
	s.mu.Lock()
	s.restoring = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.restoring = false
		s.mu.Unlock()
	}()

	sessions, injected := 0, 0
	interrupted := 0
	ver := map[string]int64{}
	for _, sess := range list {
		rec := &SessionRec{
			ID: sess.ID, AgentID: sess.AgentID, Runtime: sess.RuntimeID, Cwd: sess.Cwd,
			Title: sess.Title, Pinned: sess.Pinned, Archived: sess.Archived,
			Status: sess.Status, CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		}
		s.registerSession(rec) // restoring=true → 不重复回写 DB
		sessions++
		// interrupted 收敛：残留 in_progress（崩溃/重启中断，无终态）→ 可操作态（列表可停止/重发）。
		// 事件保持原样：续聊时 materializeRun 以「上一 run 之后」窗口重建本轮，不受影响。
		if rec.Status == SessionStatusInProgress {
			s.markSession(rec.ID, SessionStatusInterrupted)
			interrupted++
		}
	}

	// 事件按全局 seq 升序注入（跨会话也保持升序，保证 PublishAt 不倒退/不回退取号）。
	if s.bus != nil {
		var after int64
		for {
			evs, more, err := s.st.EventsAllAfter(ctx, after, 2000)
			if err != nil {
				slog.Default().Warn("restore: read events failed", "err", err)
				break
			}
			for _, ev := range evs {
				ver[ev.SessionID]++
				s.bus.PublishAt(ev.Seq, ev.Type, ev.SessionID, ver[ev.SessionID], decodePayload(ev.PayloadJSON))
				injected++
				after = ev.Seq
			}
			if !more {
				break
			}
		}
	}
	slog.Default().Info("store restored", "sessions", sessions, "events", injected)
	return sessions, injected, nil
}

// decodePayload 把 events.payload_json 解码回 map（历史/WS 载荷形态），坏 JSON 回落原文串。
func decodePayload(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

// persistSession 把会话记录同步写库（best-effort：失败告警不阻断会话流程）。
func (s *Service) persistSession(rec *SessionRec) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.st.UpsertSession(ctx, store.Session{
		ID:        rec.ID,
		AgentID:   rec.AgentID,
		RuntimeID: rec.Runtime,
		Cwd:       rec.Cwd,
		Title:     rec.Title,
		Pinned:    rec.Pinned,
		Archived:  rec.Archived,
		Status:    rec.Status,
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	})
	if err != nil {
		slog.Default().Warn("store: upsert session failed", "session", rec.ID, "err", err)
	}
}

// ——— 会话级权限记忆持久化（PermissionManager 挂钩，best-effort） ———

// persistMemoryAdd 写一条权限记忆（permission_memories 表；同键覆盖，since 为授予时刻）。
func (s *Service) persistMemoryAdd(sessionID, title, kind, decision string, since time.Time) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.AddMemory(ctx, store.Memory{
		SessionID: sessionID, Title: title, Kind: kind, Decision: decision, CreatedAt: since,
	}); err != nil {
		slog.Default().Warn("store: add permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// persistMemoryDel 撤销一条权限记忆。
func (s *Service) persistMemoryDel(sessionID, title, kind string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.st.DeleteMemory(ctx, sessionID, title, kind); err != nil {
		slog.Default().Warn("store: delete permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// persistMemoryDelAll 一键撤销整会话权限记忆。
func (s *Service) persistMemoryDelAll(sessionID string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.DeleteSessionMemories(ctx, sessionID); err != nil {
		slog.Default().Warn("store: delete session memories failed", "session", sessionID, "err", err)
	}
}

// memoryRows 读全库权限记忆（供 sqlite 启动恢复装载；无 store 时返回空）。
func (s *Service) memoryRows(ctx context.Context) ([]store.Memory, error) {
	if s.st == nil {
		return nil, nil
	}
	return s.st.ListAllMemories(ctx)
}

// ——— runs 物化（P1：一轮 prompt 的事件窗口 → runs 行；events 可重建的读模型） ———

// terminalRunStatus 终态事件类型 → run 终态。
func terminalRunStatus(typ string) string {
	switch typ {
	case BusTurnEnded:
		return SessionStatusCompleted
	case BusTurnError:
		return SessionStatusFailed
	case BusRunCancelled:
		return SessionStatusCancelled
	}
	return ""
}

// materializeRun 在 sqlite 模式下把「上一 run 之后到当前终态」的事件窗口物化为 runs 行：
// user_text 取窗口首条用户消息；first/last_seq 记录窗口（失败轮输入/统计精确回溯）。
// 幂等：窗口无新事件（重复调用/无 run）时直接返回。best-effort：失败仅告警。
func (s *Service) materializeRun(sessionID, typ string, payload map[string]any) {
	if s.st == nil {
		return
	}
	status := terminalRunStatus(typ)
	if status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	last, err := s.st.RunLastSeq(ctx, sessionID)
	if err != nil {
		slog.Default().Warn("store: run last seq failed", "session", sessionID, "err", err)
		return
	}
	evs, more, err := s.st.EventsAfter(ctx, sessionID, last, 5000)
	if err != nil {
		slog.Default().Warn("store: run window read failed", "session", sessionID, "err", err)
		return
	}
	if more {
		slog.Default().Warn("store: run window 超 5000 条被截断", "session", sessionID)
	}
	if len(evs) == 0 {
		return // 无新事件（重复终态调用等）→ 幂等跳过
	}
	firstSeq, lastSeq := evs[0].Seq, evs[len(evs)-1].Seq
	runID := fmt.Sprintf("r-%s-%d", sessionID, firstSeq)

	userText, stopReason, errCode := "", "", ""
	if payload != nil {
		if t, _ := payload["text"].(string); t != "" {
			userText = t
		}
		if st, _ := payload["stop_reason"].(string); st != "" {
			stopReason = st
		}
		if cd, _ := payload["code"].(string); cd != "" {
			errCode = cd
		}
	}
	// user_text 优先取窗口内首条用户消息。
	for _, e := range evs {
		if e.Type == BusUserMessage {
			var m map[string]any
			if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err == nil {
				if t, _ := m["text"].(string); t != "" {
					userText = t
					break
				}
			}
		}
	}
	// 首条用户消息 seq 作为 run 起点的 first_seq（更贴近「轮」语义：一轮 = 一次用户输入）。
	for _, e := range evs {
		if e.Type == BusUserMessage {
			firstSeq = e.Seq
			break
		}
	}

	started := evs[0].CreatedAt
	ended := evs[len(evs)-1].CreatedAt
	r := store.Run{
		ID: runID, SessionID: sessionID, UserText: userText, Status: status,
		ErrorCode: errCode, StopReason: stopReason,
		FirstSeq: firstSeq, LastSeq: lastSeq,
		StartedAt: started, EndedAt: ended,
	}
	if err := s.st.CreateRun(ctx, r); err != nil {
		slog.Default().Warn("store: create run failed", "session", sessionID, "run", runID, "err", err)
		return
	}
	// 终态补齐（CreateRun 直接带终态写入，FinishRun 幂等再走一遍以统一 ended 口径）。
	_ = s.st.FinishRun(ctx, runID, status, stopReason, errCode, "", ended)
	// 消息物化：同窗口 events → messages 行（user 原文 + assistant 文本片段/时间线/工具最终态）。
	s.materializeMessages(sessionID, runID, evs)
}

// ListRuns 列出某会话的轮次明细（runs 表，started_at 升序；失败轮重试/统计用）。
// 内存（无 store）模式下返回空列表（runs 是 sqlite 读模型）。
func (s *Service) ListRuns(ctx context.Context, id string) ([]handler.RunResp, error) {
	if s.st == nil {
		return []handler.RunResp{}, nil
	}
	rows, err := s.st.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]handler.RunResp, 0, len(rows))
	for _, r := range rows {
		resp := handler.RunResp{
			ID: r.ID, UserText: r.UserText, Status: r.Status,
			ErrorCode: r.ErrorCode, StopReason: r.StopReason,
			FirstSeq: r.FirstSeq, LastSeq: r.LastSeq,
			StartedAt: r.StartedAt.UTC().Format(time.RFC3339Nano),
		}
		if !r.EndedAt.IsZero() {
			resp.EndedAt = r.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, resp)
	}
	return out, nil
}

// registerSession 登记一个新会话（已存在则覆盖）；未提供的时间落为当前 UTC。
// loadTranscripts 回灌时保留原 created_at/updated_at。
// sqlite 模式下（st != nil 且非恢复过程）同步写库。
func (s *Service) registerSession(rec *SessionRec) {
	s.mu.Lock()
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	s.sessions[rec.ID] = rec
	restoring := s.restoring
	s.mu.Unlock()
	if !restoring {
		s.persistSession(rec)
	}
}

// sessionRec 返回某会话的进程内记录副本（断点恢复/内部查询用；不存在返回 nil）。
func (s *Service) sessionRec(id string) *SessionRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[id]
	if !ok {
		return nil
	}
	cp := *rec
	return &cp
}

// markSession 更新会话终态（sqlite 模式下同步写库 status）。
func (s *Service) markSession(id, status string) {
	s.mu.Lock()
	if r, ok := s.sessions[id]; ok {
		r.Status = status
		r.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	if s.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateSessionMeta(ctx, id, store.SessionMetaPatch{Status: &status}); err != nil {
			slog.Default().Warn("store: mark session failed", "session", id, "err", err)
		}
	}
}

// publish 把域事件发布进 eventbus（agg = sessionID；每 agg 版本递增），
// 并旁路追加到该会话的 wire transcript（未启用/非 map 载荷时跳过）。
// sqlite 模式下（st != nil）：事件先经 store.AppendEvent 在事务内取全局 seq，
// bus 以 PublishAt 镜像同一号序（跨重启连续）；DB 写失败时退化进程内取号并告警（不阻断会话）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (s *Service) publish(typ, sessionID string, payload any) eventbus.Event {
	s.mu.Lock()
	s.ver[sessionID]++
	v := s.ver[sessionID]
	s.mu.Unlock()

	bus := s.bus
	seq := int64(0)
	if st := s.st; st != nil {
		got, err := st.AppendEvent(context.Background(), sessionID, typ, payload)
		if err != nil {
			slog.Default().Warn("store: append event failed（退化为进程内取号）",
				"session", sessionID, "type", typ, "err", err)
		} else {
			seq = got
		}
	}
	var evt eventbus.Event
	if bus == nil {
		evt = eventbus.Event{Type: typ, AggregID: sessionID, Version: v, Seq: seq}
	} else if seq > 0 {
		evt = bus.PublishAt(seq, typ, sessionID, v, payload)
	} else {
		evt = bus.Publish(typ, sessionID, v, payload)
	}
	if m, ok := payload.(map[string]any); ok {
		s.publishSideEffects(sessionID, typ, m, evt.Seq)
	}
	// 终态事件 → runs 物化（sqlite 模式；窗口无新事件时幂等跳过）。
	if s.st != nil && terminalRunStatus(typ) != "" {
		if m, ok := payload.(map[string]any); ok {
			s.materializeRun(sessionID, typ, m)
		}
	}
	return evt
}

// publishSideEffects 非 map 载荷不触发的旁路副作用：
// wire transcript 追加 + 会话标题自动推导（首个用户消息；PATCH 可覆盖）。
func (s *Service) publishSideEffects(sessionID, typ string, m map[string]any, seq int64) {
	s.tx.appendLine(sessionID, seq, typ, m)
	// 会话标题自动推导：首个用户消息作为标题（截断）。PATCH 可覆盖（见 UpdateSession）。
	// 须持 s.mu 写 Title（与 UpdateSession/sessionResp 并发读写作同步，避免数据竞争）。
	if typ == BusUserMessage {
		var derived string
		s.mu.Lock()
		if r, ok := s.sessions[sessionID]; ok && strings.TrimSpace(r.Title) == "" {
			if text, _ := m["text"].(string); text != "" {
				derived = deriveTitle(text)
				r.Title = derived
				r.UpdatedAt = time.Now().UTC()
			}
		}
		s.mu.Unlock()
		if derived != "" && s.st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.st.UpdateSessionMeta(ctx, sessionID, store.SessionMetaPatch{Title: &derived}); err != nil {
				slog.Default().Warn("store: derive title persist failed", "session", sessionID, "err", err)
			}
		}
	}
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
// 截断按 rune 数判断（len() 是字节数，CJK 一个字符占 3 字节：按字节判断会
// 在「字节够长但字符不足 48」时对 runes[:48] 越界 panic）。
func deriveTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 48 {
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
