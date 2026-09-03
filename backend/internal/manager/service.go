package manager

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// AgentRec 是进程内 Agent 定义（M0 为每个已注册 runtime 播种一个默认 Agent）。
type AgentRec struct {
	ID      string
	Name    string
	Runtime string
	State   string
}

// SessionRec 是进程内会话终态记录（M0 内存态；M2 落 SQLite 后由 events 表回放）。
type SessionRec struct {
	ID        string
	AgentID   string
	Runtime   string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service 实现 handler.Service（替换 mock），为控制面列表提供「真实」数据：
// runtime 来自注册表；agent 为每 runtime 的默认播种；会话来自本进程内存存储。
type Service struct {
	reg *runtime.Registry
	bus *eventbus.Bus

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
		out = append(out, handler.RuntimeResp{Name: sp.ID, Version: sp.Version})
	}
	return out, nil
}

// ListSessions 返回本进程内全部会话（按创建时间倒序）。
func (s *Service) ListSessions() ([]handler.SessionResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]handler.SessionResp, 0, len(s.sessions))
	for _, r := range s.sessions {
		out = append(out, handler.SessionResp{
			ID:      r.ID,
			AgentID: r.AgentID,
			Runtime: r.Runtime,
			Status:  r.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
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

// publish 把域事件发布进 eventbus（agg = sessionID；每 agg 版本递增）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (s *Service) publish(typ, sessionID string, payload any) eventbus.Event {
	s.mu.Lock()
	s.ver[sessionID]++
	v := s.ver[sessionID]
	s.mu.Unlock()

	bus := s.bus
	if bus == nil {
		return eventbus.Event{}
	}
	return bus.Publish(typ, sessionID, v, payload)
}

// requireAgent 供 Orchestrator 用 runtimeID 取播种 Agent。
func (s *Service) requireAgent(runtimeID string) (*AgentRec, error) {
	a, ok := s.agentForRuntime(runtimeID)
	if !ok {
		return nil, fmt.Errorf("runtime %q 无默认 Agent（注册表未含该 runtime）", runtimeID)
	}
	return a, nil
}
