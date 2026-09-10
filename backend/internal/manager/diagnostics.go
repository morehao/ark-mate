package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/routing"
	"github.com/morehao/ark-forge/internal/runtime"
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
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("未知 agent %q（若它是已被删除的 agent：用同一个 id 重新注册即可让绑定它的会话复活）", id), nil)
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
	var spec runtime.Spec
	specOK := false
	if s.reg != nil {
		if sp, ok := s.reg.Get(runtimeID); ok {
			spec, specOK = sp, true
			resp.Command = sp.Launch.Command
			resp.RuntimeName = sp.Name
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
	resp.Remediation = s.remediationFor(agent, spec, specOK, resp.Connected, resp.StderrTail, resp.LastError != nil)
	return resp, nil
}

// clearAgentLastError 清除某 agent 的最近失败快照（agent 被删除时调用：同 id 重建后不回显旧错）。
func (s *Service) clearAgentLastError(agentID string) {
	s.diagMu.Lock()
	delete(s.lastErrs, agentID)
	s.diagMu.Unlock()
}

// remediationFor 推导该 agent 当前的可执行修复步骤（诊断面给「怎么修」，而不是只有报错）。
//
// 事实来源（按序合并、去重）：
//  1. runtime 未注册 / 已注册但启动命令不可执行（与路由用同一判定 routing.Installed）；
//  2. provider 档案悬空或无密钥（CredentialProblem 原文，已含出路）；
//  3. 已连接时底座自报的鉴权方式（caps.AuthGuidance，如「在终端执行 xxx 登录」）；
//  4. 最近失败的 stderr 呈鉴权特征 → 指向 provider 档案/底座登录；
//  5. 该 runtime 声明的 SetupSteps（安装/登录步骤）——仅在未连接或已有问题时并入，
//     健康快照不掺安装说明。
//
// 供 /agents/:id/diagnostics 的 remediation 字段消费；不臆造命令：文本只来自上述事实来源。
func (s *Service) remediationFor(agent *AgentRec, spec runtime.Spec, specOK, connected bool,
	stderr []string, hasLastErr bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(step string) {
		step = strings.TrimSpace(step)
		if step == "" || seen[step] {
			return
		}
		seen[step] = true
		out = append(out, step)
	}

	if !specOK {
		add(fmt.Sprintf("runtime %q 未注册：确认底座已注册（`forge config --json` 可查 runtimes 来源）或重新注册该 runtime",
			agent.pickRuntime()))
	} else if !routing.Installed(spec) {
		add(fmt.Sprintf("runtime %q 的启动命令 %q 当前不可执行：安装该底座并确保它在 PATH 中（或把配置改成绝对路径）",
			spec.ID, spec.Launch.Command))
	}
	if err := s.CredentialProblem(agent); err != nil {
		if ae, ok := acl.As(err); ok && ae != nil {
			add(ae.Details)
		}
	}
	if connected {
		for _, g := range s.authGuidanceFor(agent.ID) {
			desc := g.Description
			if desc == "" {
				desc = g.Name
			}
			if len(g.Command) > 0 {
				desc += fmt.Sprintf("（命令 %s）", strings.Join(g.Command, " "))
			}
			add("底座声明的鉴权方式：" + desc)
		}
	}
	if hasLastErr && looksLikeAuthFailure(stderr) {
		add("底座报告鉴权失败：给该 agent 绑定带 key 的 provider 档案，或先在底座内完成登录")
	}
	if specOK && len(spec.SetupSteps) > 0 && (!connected || hasLastErr || len(out) > 0) {
		for _, step := range spec.SetupSteps {
			add(step)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// looksLikeAuthFailure stderr 是否呈鉴权失败特征（保守匹配关键词，避免把普通报错误判成鉴权问题）。
func looksLikeAuthFailure(lines []string) bool {
	keywords := []string{"401", "403", "unauthorized", "authentication", "api key", "api_key", "invalid_api_key"}
	for _, l := range lines {
		t := strings.ToLower(l)
		for _, k := range keywords {
			if strings.Contains(t, k) {
				return true
			}
		}
	}
	return false
}

// LiveClient 返回某 runtime 当前连接（未断开时）；pid 为子进程号（0 = 未登记）。
func (o *Orchestrator) LiveClient(runtimeID string) (*acl.Client, int, bool) {
	o.mu.Lock()
	var rc *runtimeConn
	for _, c := range o.conns {
		if c.spec.ID == runtimeID && c.client != nil {
			rc = c
			break
		}
	}
	o.mu.Unlock()
	if rc == nil {
		return nil, 0, false
	}
	select {
	case <-rc.client.Done():
		return nil, 0, false
	default:
		return rc.client, rc.pid, true
	}
}
