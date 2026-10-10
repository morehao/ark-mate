package manager

import (
	"context"
	"fmt"
	"sort"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/routing"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// defaultAgentID 内置种子 Agent 的 ID：未显式切换「当前 Agent」时的回落值。
// 「当前 Agent」（可切换、可落盘）见 default_agent.go —— 它不是这个常量，而是
// Service.currentAgent 经回落链（显式选择 > 本常量 > id 升序首个）算出的。
const defaultAgentID = "dsh"

// builtinAgents 内置 Agent 播种表：每个内置 runtime 一个 Agent（dsh 为默认）。
// 内置 runtime 不预置 agent（agent 定义由用户/内置 definitions 显式给出）。
var builtinAgents = []struct {
	id      string
	name    string
	runtime string
}{
	{id: "dsh", name: "dsh", runtime: "deepseekharness"},
	{id: "opencode", name: "opencode", runtime: "opencode"},
}

// ResolveRuntime 依 agent 路由候选做 FirstAvailable 路由（§7 切片 4）：候选顺序 → 已注册 +
// 启动命令可执行。全部未采用时按分类报错：全未注册 → runtime_not_found（单候选场景与既有
// 语义一致）；存在已注册但命令不可执行 → runtime_unavailable。空候选 → no_runtime。
func (s *Service) ResolveRuntime(agent *AgentRec) (string, error) {
	if len(agent.Runtimes) == 0 {
		return "", acl.NewError(acl.ClassRequest, "no_runtime",
			fmt.Sprintf("agent %q 未配置 runtime 候选", agent.ID), nil)
	}
	strategy, known := routing.ParseStrategy(agent.RoutingPolicy)
	if !known {
		// 未知声明回退默认（design doc §15：宽容处理，不因坏策略拒绝会话）。
		strategy = routing.StrategyFirstAvailable
	}
	var (
		id string
		av routing.Availability
	)
	if strategy == routing.StrategyCapabilityRouted {
		id, av = routing.CapabilityRouted(s.reg, agent.Runtimes, s.requiredMcpTransports(agent))
	} else {
		id, av = routing.FirstAvailable(s.reg, agent.Runtimes)
	}
	if id != "" {
		return id, nil
	}
	if len(av.CapabilityMismatch) > 0 {
		return "", acl.NewError(acl.ClassRequest, "runtime_capability_mismatch",
			fmt.Sprintf("agent %q 候选 runtime 均不满足所需 MCP transport：未注册 %v；命令不可执行 %v；能力不符 %v",
				agent.ID, av.NotFound, av.NotInstalled, av.CapabilityMismatch), nil)
	}
	if len(av.NotInstalled) == 0 {
		return "", acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("agent %q 候选 runtime 均未注册：%v（检查底座是否已注册：`mate config --json` 看 runtimes 来源；"+
				"修复步骤见 GET /v1/mate/agents/%s/diagnostics 的 remediation）", agent.ID, av.NotFound, agent.ID), nil)
	}
	return "", acl.NewError(acl.ClassRequest, "runtime_unavailable",
		fmt.Sprintf("agent %q 候选 runtime 均不可用：未注册 %v；命令不可执行 %v（命令不可执行 = 已注册但 CLI 不在 PATH："+
			"安装底座或改为绝对路径后重试）", agent.ID, av.NotFound, av.NotInstalled), nil)
}

// requiredMcpTransports 由 agent 显式 MCP 白名单推导「能力路由」所需的 transport 集合
// （§7 CapabilityRouted）：取白名单 ∩ 能力数据面注册表中的 server transport 去重。
// Mcps == nil（未声明/全量）不构成硬约束 → 返回 nil（等价 FirstAvailable 的可选性：
// 全量注入时由各 runtime 自身传输过滤兜底，不做路由排除）。
func (s *Service) requiredMcpTransports(agent *AgentRec) []string {
	if agent.Mcps == nil || s.caps == nil {
		return nil
	}
	want := make(map[string]bool, len(*agent.Mcps))
	for _, id := range *agent.Mcps {
		want[id] = true
	}
	var out []string
	seen := make(map[string]bool)
	gateway := s.McpGatewayEnabled()
	for _, m := range s.caps.ListMcpServers() {
		if !want[m.ID] || seen[m.Transport] {
			continue
		}
		// M7-3：网关在环时，远程传输不再构成**路由**硬约束——它对每个 runtime 都可达（平台代连），
		// 再拿它去排除候选 runtime 会把"本来能跑"的底座判成 CapabilityMismatch。
		if gateway && capability.IsRemoteTransport(m.Transport) {
			continue
		}
		seen[m.Transport] = true
		out = append(out, m.Transport)
	}
	return out
}

// currentDefaultLocked 计算生效的「当前 Agent」id（须持 s.mu）：
// 显式选择（agent 仍存在）> 内置种子 defaultAgentID > id 升序首个；注册表为空 → 空串。
func (s *Service) currentDefaultLocked() string {
	if s.currentAgent != "" {
		if _, ok := s.agents[s.currentAgent]; ok {
			return s.currentAgent
		}
	}
	if _, ok := s.agents[defaultAgentID]; ok {
		return defaultAgentID
	}
	best := ""
	for id := range s.agents {
		if best == "" || id < best {
			best = id
		}
	}
	return best
}

// ListAgents 返回播种的全部 Agent（is_default 标出「当前 Agent」，conversation_count 为删除影响面）。
//
// `default_model` 是**派生只读**（E7）：每个 runtime 的默认模型取 `models.is_default`，
// 按该 agent 的 `runtimes[]` 求交。读库在**持锁之前**完成一次（不是逐 agent 查）——
// 在 s.mu 里做 IO 会让整个注册表面等服务层的数据库调用。
func (s *Service) ListAgents() ([]dtoagent.AgentResp, error) {
	defaults := s.defaultModelByRuntime(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.currentDefaultLocked()
	out := make([]dtoagent.AgentResp, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, dtoagent.AgentResp{ID: a.ID, Name: a.Name, Provider: a.Provider, State: a.State,
			IsDefault: a.ID == cur, Seed: seedAgent(a.ID), Runtimes: a.Runtimes,
			PermissionPolicy:   toPermissionPolicyResp(a.PermissionPolicy),
			ConversationCount:  s.conversationCountLocked(a.ID),
			DefaultModel:       derivedDefaultModels(a.Runtimes, defaults),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// conversationCountLocked 统计绑定该 agent 的会话数（调用方须持 s.mu）。
// 用于 /agents 列表与删除影响面：归属不可变 → 删除时这些会话都会失去执行者。
func (s *Service) conversationCountLocked(agentID string) int {
	if agentID == "" {
		return 0
	}
	n := 0
	for _, rec := range s.conversations {
		if rec != nil && rec.AgentID == agentID {
			n++
		}
	}
	return n
}

// ListRuntimes 返回注册表全部 runtime（含静态能力基线）。
func (s *Service) ListRuntimes() ([]dtoruntime.RuntimeResp, error) {
	specs := s.reg.List()
	out := make([]dtoruntime.RuntimeResp, 0, len(specs))
	for _, sp := range specs {
		out = append(out, dtoruntime.RuntimeResp{
			Name: sp.ID,
			ID:   sp.ID,
			Vers: sp.Version,
			Caps: capabilityKVs(sp.Caps),
		})
	}
	return out, nil
}

// capabilityKVs 把 runtime.Capabilities 展开为 key/value 列表（稳定序）。
func capabilityKVs(c runtime.Capabilities) []dtoruntime.CapabilityKV {
	return []dtoruntime.CapabilityKV{
		{Key: "sessions_persistent", Value: c.SessionsPersistent},
		{Key: "multi_session_concurrent", Value: c.MultiSessionConcurrent},
		{Key: "images", Value: c.Images},
		{Key: "embedded_context", Value: c.EmbeddedContext},
	}
}

// defaultAgent 返回「当前 Agent」（用户可切换；缺省回落内置种子 dsh，见 default_agent.go）。
func (s *Service) defaultAgent() (*AgentRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[s.currentDefaultLocked()]
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

// requireDefaultAgent 供 Orchestrator 取「当前 Agent」。
func (s *Service) requireDefaultAgent() (*AgentRec, error) {
	a, ok := s.defaultAgent()
	if !ok {
		return nil, fmt.Errorf("无可用 Agent（注册表为空）：请先播种或注册 agent（当前 Agent 可切换）")
	}
	return a, nil
}

// resolveConversationAgent 归一新建会话的 agent_id：空 → 「当前 Agent」；非空 → 原样。
func (s *Service) resolveConversationAgent(agentID string) (string, error) {
	if agentID != "" {
		return agentID, nil
	}
	def, err := s.requireDefaultAgent()
	if err != nil {
		return "", err
	}
	return def.ID, nil
}

// AgentByID 按 ID 取 Agent（会话动作/REST 用）。
func (s *Service) AgentByID(id string) (*AgentRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	return a, ok
}

// ConversationAgentID 返回会话归属的 agent id（无此会话 → 空串）。供权限策略等按 agent 判定。
func (s *Service) ConversationAgentID(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.conversations[id]; ok {
		return r.AgentID
	}
	return ""
}
