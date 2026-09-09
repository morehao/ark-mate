package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// maxDiagStderrLines 外露的 stderr 诊断行数上限（超出保留最近若干行）。
const maxDiagStderrLines = 20

// agentLastErr 某 agent 最近一次失败（进程起不来/握手失败/轮次失败）的诊断快照。
type agentLastErr struct {
	At             time.Time
	ConversationID string
	Code           string
	Class          string
	Details        string
	StderrTail     []string
}

// recordAgentError 记录 agent 最近一次失败（含 stderr 诊断尾巴）。agentID 为空时按会话反查；
// 反查不到则只记在会话维度（不影响外露的其余字段）。
func (s *Service) recordAgentError(agentID, conversationID string, ae *acl.Error, tail []string) {
	if ae == nil {
		return
	}
	if agentID == "" && conversationID != "" {
		agentID = s.ConversationAgentID(conversationID)
	}
	if agentID == "" {
		return
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.lastErrs == nil {
		s.lastErrs = map[string]agentLastErr{}
	}
	s.lastErrs[agentID] = agentLastErr{
		At:             time.Now().UTC(),
		ConversationID: conversationID,
		Code:           ae.Code,
		Class:          string(ae.Class),
		Details:        ae.Details,
		StderrTail:     acl.TailLines(tail, maxDiagStderrLines),
	}
}

// agentLastError 读取某 agent 的最近失败快照。
func (s *Service) agentLastError(agentID string) (agentLastErr, bool) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	le, ok := s.lastErrs[agentID]
	return le, ok
}

// AgentDiagnostics 返回某 agent 的诊断快照（A3：stderr 诊断尾巴外露）。
//
// stderr_tail 来源优先级：live（当前连接子进程的实时尾巴）→ last_error（上次失败时采集的尾巴）
// → 空（无连接且无失败记录）。stderr_source 标明来源，前端/排障不必猜。
func (s *Service) AgentDiagnostics(_ context.Context, id string) (*handler.AgentDiagnosticsResp, error) {
	agent, ok := s.AgentByID(id)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	runtimeID := agent.pickRuntime()
	resp := &handler.AgentDiagnosticsResp{
		AgentID:      id,
		Name:         agent.Name,
		State:        agent.State,
		Runtime:      runtimeID,
		StderrTail:   []string{},
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		StderrSource: "none",
	}
	if s.reg != nil {
		if spec, ok := s.reg.Get(runtimeID); ok {
			resp.Command = spec.Launch.Command
			resp.RuntimeName = spec.Name
		}
	}
	if s.orch != nil {
		if cli, pid, live := s.orch.LiveClient(runtimeID); live {
			resp.Connected = true
			resp.PID = pid
			if tail := acl.TailLines(cli.StderrTail(), maxDiagStderrLines); len(tail) > 0 {
				resp.StderrTail = tail
				resp.StderrSource = "live"
			}
		}
	}
	if le, ok := s.agentLastError(id); ok {
		resp.LastError = &handler.AgentLastErrorResp{
			At:             le.At.Format(time.RFC3339),
			ConversationID: le.ConversationID,
			Code:           le.Code,
			Class:          le.Class,
			Details:        le.Details,
			StderrTail:     le.StderrTail,
		}
		if len(resp.StderrTail) == 0 && len(le.StderrTail) > 0 {
			resp.StderrTail = le.StderrTail
			resp.StderrSource = "last_error"
		}
	}
	return resp, nil
}

// LiveClient 返回某 runtime 当前连接（未断开时）；pid 为子进程号（0 = 未登记）。
func (o *Orchestrator) LiveClient(runtimeID string) (*acl.Client, int, bool) {
	o.mu.Lock()
	rc := o.conns[runtimeID]
	o.mu.Unlock()
	if rc == nil || rc.client == nil {
		return nil, 0, false
	}
	select {
	case <-rc.client.Done():
		return nil, 0, false
	default:
		return rc.client, rc.pid, true
	}
}
