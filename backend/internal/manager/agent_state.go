package manager

import (
	"context"
	"fmt"
	"sort"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// defaultAgentID 未显式切换「当前 Agent」时的回落种子 id。
//
// 它**由构建期选定的底座派生**（种子 agent 的 id 与它服务的 runtime id 同形，见 builtinAgents）。
// 为什么不做成常量（历史实现恒为 `"dsh"`）：底座不是 dsh 的构建下把 `dsh` 当回落值会指向一个
// 不存在的 agent，`requireDefaultAgent` 会报「无可用 Agent」——那是个假错误（分明确实有一个
// 可用的 agent）。
func defaultAgentID() string {
	sel := runtime.SelectedRuntime()
	for _, b := range builtinAgents {
		if b.runtime == sel {
			return b.id
		}
	}
	return sel
}

// builtinAgents 内置 Agent 播种表：每个内置 runtime 一个 Agent。
//
// **它必须与 `runtime.AllBuiltinSpecs()` 逐条对齐**——"内置清单"只有一个事实源，多一条会播出一个
// 指向未装配底座的 agent（建会话时才报 runtime_not_found），少一条会让一个内置底座没有可用 agent、
// 而 `defaultAgentID()` 回落成那个 id 之后 `requireDefaultAgent` 报「无可用 Agent」。
// `TestBuiltinAgentSeedsMatchBuiltinSpecs` 钉住这条对齐（含"id 与 runtime 同形"这一点——
// `defaultAgentID()` 直接返回 runtime id，两者不同形时它会返回一个不存在的 agent id）。
var builtinAgents = []struct {
	id      string
	name    string
	runtime string
}{
	{id: "dsh", name: "dsh", runtime: "dsh"},
}

// ResolveRuntime 返回**构建期选定的**底座 id，并校验它确实已装配。
//
// 历史：这里曾是 FirstAvailable / CapabilityRouted 路由引擎（按 agent 声明的 runtime 候选试探）。
// 候选面随产品约束（用户不能选择 runtime）下线之后，路由的每个输入都成了常量——引擎只剩
// 「取那一个 + 校验已装配」这一步。
func (s *Service) ResolveRuntime() (string, error) {
	spec, err := s.selectedSpec()
	if err != nil {
		return "", err
	}
	return spec.ID, nil
}

// selectedSpec 取构建期选定的底座 spec；未装配 → 明确错误（不静默回落、不返回空串）。
//
// 正常路径由装配期的 `runtime.ProductRegistry` fail-closed 拦住（见 internal/app 与
// internal/gateway）；这里是"注册表被别处动过"时的兜底判据——返回空串会让调用方拿到一个
// 没有底座的会话，错误会推迟到某个更远的地方才出现。
func (s *Service) selectedSpec() (runtime.Spec, error) {
	id := runtime.SelectedRuntime()
	spec, ok := s.reg.Get(id)
	if !ok {
		return runtime.Spec{}, acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("构建期选定的 runtime %q 未注册（检查 -ldflags 注入的 pkg/runtime.BuildRuntime "+
				"与 .ark-mate/config/runtimes.json）", id), nil)
	}
	return spec, nil
}

// currentDefaultLocked 计算生效的「当前 Agent」id（须持 s.mu）：
// 显式选择（agent 仍存在）> 构建期底座对应的种子 agent > id 升序首个；注册表为空 → 空串。
func (s *Service) currentDefaultLocked() string {
	if s.currentAgent != "" {
		if _, ok := s.agents[s.currentAgent]; ok {
			return s.currentAgent
		}
	}
	if id := defaultAgentID(); id != "" {
		if _, ok := s.agents[id]; ok {
			return id
		}
	}
	best := ""
	for id := range s.agents {
		if best == "" || id < best {
			best = id
		}
	}
	return best
}

// ListAgents 返回播种的全部 Agent（is_default 标出「当前 Agent」，session_count 为删除影响面）。
//
// `default_model` 是**派生只读**（E7）：取 `models.is_default` 里**构建期底座**那一份。
// 读库在**持锁之前**完成一次（不是逐 agent 查）——在 s.mu 里做 IO 会让整个注册表面等服务层的
// 数据库调用。
func (s *Service) ListAgents() ([]dtoagent.AgentResp, error) {
	defaults := s.defaultModelByRuntime(context.Background())
	// 底座是构建期常量：每个 agent 的派生默认模型都来自同一份（不再按 agent 各自声明求交）。
	selected := []string{runtime.SelectedRuntime()}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.currentDefaultLocked()
	out := make([]dtoagent.AgentResp, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, dtoagent.AgentResp{ID: a.ID, Name: a.Name, Provider: a.Provider, State: a.State,
			IsDefault: a.ID == cur, Seed: seedAgent(a.ID),
			PermissionPolicy: toPermissionPolicyResp(a.PermissionPolicy),
			SessionCount:     s.sessionCountLocked(a.ID),
			DefaultModel:     derivedDefaultModels(selected, defaults),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// sessionCountLocked 统计绑定该 agent 的会话数（调用方须持 s.mu）。
// 用于 /agents 列表与删除影响面：归属不可变 → 删除时这些会话都会失去执行者。
func (s *Service) sessionCountLocked(agentID string) int {
	if agentID == "" {
		return 0
	}
	n := 0
	for _, rec := range s.sessions {
		if rec != nil && rec.AgentID == agentID {
			n++
		}
	}
	return n
}

// defaultAgent 返回「当前 Agent」（用户可切换；缺省回落构建期底座对应的种子，见 default_agent.go）。
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

// resolveSessionAgent 归一新建会话的 agent_id：空 → 「当前 Agent」；非空 → 原样。
func (s *Service) resolveSessionAgent(agentID string) (string, error) {
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

// SessionAgentID 返回会话归属的 agent id（无此会话 → 空串）。供权限策略等按 agent 判定。
func (s *Service) SessionAgentID(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.sessions[id]; ok {
		return r.AgentID
	}
	return ""
}
