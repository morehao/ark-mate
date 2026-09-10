package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// ——— Agent Registry（P2）：definitions 目录 + 注册/编辑/删除/启停 ———
//
// 定义文件：<AgentDir>/<id>.json，单文件一个 agent（JSON 对象），serve 启动时加载合并到种子。
// 种子 agent（agent-<runtime>）只读可改元数据（name/provider），不可删/改 runtime；
// 用户 agent 可自由 CRUD。Agent 是对既有 runtime 的一个命名配置（provider 供注入面使用，
// 进程级生命周期仍是 runtime 粒度：单 runtime 至多一条连接，启停即该 runtime 进程）。

const (
	reservedAgentPrefix = "agent-"
	agentIDPattern      = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`
)

var agentIDRE = regexp.MustCompile(agentIDPattern)

// agentDefinitionFile 磁盘上的 agent 定义。
type agentDefinitionFile struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Runtimes []string          `json:"runtimes"`           // 路由候选 runtime（P0 取首候选）
	Provider string            `json:"provider,omitempty"` // provider 档案 id（P2 provider 注入面）；可为空
	Env      map[string]string `json:"env,omitempty"`      // 预留：按 agent 附加 env（本轮不注入，仅持久化）
	// Mcps 启用的 MCP server id 白名单（P2 §5.2）。缺省 = 未声明（全启用按 runtime 传输
	// 过滤）；`mcps: []` = 本 agent 禁用 MCP 注入。指针以区分「未声明」与「空白名单」。
	Mcps *[]string `json:"mcps,omitempty"`
	// RoutingPolicy 路由策略（P2 §7）：first_available（默认）| capability_routed。
	// 缺省/空 = first_available；capability_routed 下若声明 Mcps 白名单，则只在能承载
	// 所需 MCP transport 的 runtime 里选。未知值回退默认（解析见 Service.ResolveRuntime）。
	RoutingPolicy string `json:"routing_policy,omitempty"`
	// PermissionPolicy 默认权限策略（M2 #7）：缺省 = 全 ask（行为与历史一致）。
	PermissionPolicy *PermissionPolicy `json:"permission_policy,omitempty"`
}

// LoadAgentDefinitions 加载 definitions 目录并合并到 agents 表（启动时调用；
// dir 为空 = 不使用 definitions）。返回加载数量；坏文件/未知 runtime 跳过并告警。
func (s *Service) LoadAgentDefinitions(dir string) int {
	s.mu.Lock()
	s.agentDir = dir
	s.mu.Unlock()
	if dir == "" {
		return 0
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		slog.Warn("agents: 扫描 definitions 失败", "dir", dir, "err", err)
		return 0
	}
	loaded := 0
	for _, p := range files {
		base := filepath.Base(p)
		if base == "providers.json" || base == defaultAgentFile {
			continue // 目录级配置（provider 档案 / 当前 Agent 选择），非 agent 定义
		}
		b, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("agents: 读取定义失败", "file", p, "err", err)
			continue
		}
		var d agentDefinitionFile
		if err := json.Unmarshal(b, &d); err != nil {
			slog.Warn("agents: 定义 JSON 非法", "file", p, "err", err)
			continue
		}
		if err := s.applyDefinition(d, true); err != nil {
			slog.Warn("agents: 定义未采纳", "file", p, "err", err)
			continue
		}
		loaded++
	}
	if loaded > 0 {
		slog.Info("agents: definitions 已加载", "count", loaded, "dir", dir)
	}
	// provider 档案（同目录 providers.json）随 definitions 一并装载。
	s.loadProvidersFile(dir)
	// 「当前 Agent」的显式选择（同目录 default.json）在定义装载后解析（需先有 agent 才可校验）。
	s.loadDefaultAgentFile(dir)
	return loaded
}

// applyDefinition 合并一条定义进 agents 表（启动静默 / 运行期需鉴权由调用方保证）。
func (s *Service) applyDefinition(d agentDefinitionFile, silent bool) error {
	if !agentIDRE.MatchString(d.ID) {
		return acl.NewError(acl.ClassRequest, "invalid_agent_id", fmt.Sprintf("id 非法（须 %s）", agentIDPattern), nil)
	}
	for _, r := range d.Runtimes {
		if _, ok := s.reg.Get(r); !ok {
			return acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", r), nil)
		}
	}
	if err := d.PermissionPolicy.Validate(); err != nil {
		return acl.NewError(acl.ClassRequest, "invalid_permission_policy", err.Error(), nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, exists := s.agents[d.ID]; exists {
		if !silent {
			return acl.NewError(acl.ClassRequest, "agent_exists", fmt.Sprintf("agent %q 已存在", d.ID), nil)
		}
		if d.Name != "" {
			cur.Name = d.Name
		}
		if len(d.Runtimes) > 0 {
			cur.Runtimes = d.Runtimes
		}
		if d.Provider != "" {
			cur.Provider = d.Provider
		}
		if d.Mcps != nil {
			cur.Mcps = d.Mcps
		}
		if d.RoutingPolicy != "" {
			cur.RoutingPolicy = d.RoutingPolicy
		}
		if d.PermissionPolicy != nil {
			cur.PermissionPolicy = d.PermissionPolicy
		}
		return nil
	}
	s.agents[d.ID] = &AgentRec{
		ID: d.ID, Name: d.Name, Runtimes: d.Runtimes, Provider: d.Provider, Mcps: d.Mcps,
		RoutingPolicy: d.RoutingPolicy, PermissionPolicy: d.PermissionPolicy, State: AgentStateStopped,
	}
	return nil
}

// isBuiltinAgent 是否为内置播种 agent（dsh/opencode；随 builtinAgents 单一事实源）。
func isBuiltinAgent(id string) bool {
	for _, b := range builtinAgents {
		if b.id == id {
			return true
		}
	}
	return false
}

// seedAgent 种子判定（对外只读）：内置播种 agent 或历史保留前缀 agent-。
// 修复：内置播种 id 由 agent-<runtime> 改为 dsh/opencode 后，仅按前缀判定的守卫失效，
// 导致种子可被 DELETE、runtime 可被 PATCH 改（README 与代码注释均声明只读）。
func seedAgent(id string) bool {
	return isBuiltinAgent(id) || strings.HasPrefix(id, reservedAgentPrefix)
}

// CreateAgent 注册新 agent（非种子）：校验可启动（runtime 已注册 → 静态能力冻结于 runtime 侧）并持久化。
// 返回注册后的对外表示。
func (s *Service) CreateAgent(_ context.Context, req handler.AgentCreateReq) (*handler.AgentResp, error) {
	if strings.HasPrefix(req.ID, reservedAgentPrefix) {
		return nil, acl.NewError(acl.ClassRequest, "reserved_agent_id", fmt.Sprintf("%q 为种子保留前缀，自定义 agent 请用其它 id", reservedAgentPrefix), nil)
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	d := agentDefinitionFile{ID: req.ID, Name: req.Name, Runtimes: req.Runtimes, Provider: req.Provider,
		PermissionPolicy: fromPermissionPolicyResp(req.PermissionPolicy)}
	if err := s.applyDefinition(d, false); err != nil {
		return nil, err
	}
	if err := s.persistAgentDef(d); err != nil {
		// 回滚内存登记
		s.mu.Lock()
		delete(s.agents, req.ID)
		s.mu.Unlock()
		return nil, err
	}
	return s.agentRespLocked(req.ID), nil
}

// UpdateAgent 更新 agent 元数据（name/runtimes/provider 子集；provider 传空串 = 清除）。
func (s *Service) UpdateAgent(_ context.Context, id string, patch handler.AgentUpdateReq) (*handler.AgentResp, error) {
	s.mu.Lock()
	cur, ok := s.agents[id]
	if !ok {
		s.mu.Unlock()
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	if patch.Runtimes != nil && len(*patch.Runtimes) > 0 {
		if seedAgent(id) {
			s.mu.Unlock()
			return nil, acl.NewError(acl.ClassRequest, "seed_agent_readonly",
				fmt.Sprintf("种子 agent %q 的 runtime 不可修改（可直接编辑 definitions 文件）", id), nil)
		}
		for _, r := range *patch.Runtimes {
			if _, rok := s.reg.Get(r); !rok {
				s.mu.Unlock()
				return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", r), nil)
			}
		}
		cur.Runtimes = *patch.Runtimes
	}
	if patch.Name != nil {
		cur.Name = *patch.Name
	}
	if patch.Provider != nil {
		cur.Provider = *patch.Provider
	}
	if patch.PermissionPolicy != nil {
		pol := fromPermissionPolicyResp(patch.PermissionPolicy)
		if err := pol.Validate(); err != nil {
			s.mu.Unlock()
			return nil, acl.NewError(acl.ClassRequest, "invalid_permission_policy", err.Error(), nil)
		}
		cur.PermissionPolicy = pol
	}
	s.mu.Unlock()
	if err := s.syncAgentDef(id); err != nil {
		return nil, err
	}
	return s.agentRespLocked(id), nil
}

// DeleteAgent 删除用户 agent（种子不可删）：返回影响面（绑定会话数 / 被摘除的活跃会话数 /
// 是否「当前 Agent」）。
//
// 语义（归属不可变 + 删除立刻生效）：
//   - 存量会话保留可读（历史仍在，归属不被改写、也不静默改绑到别的 agent——那会连带静默换掉
//     权限面/MCP 白名单/原生上下文），但其上继续提问将报 agent_not_found（文案给出「同 id 重建 /
//     导出历史」两条出路）；
//   - 内存里仍活跃的会话在此刻被摘除，避免「删了还能跑、重启后才失效」的半死态；
//   - 有在途 turn 时整体拒绝（agent_busy）：失败即无副作用，不删一半。
func (s *Service) DeleteAgent(_ context.Context, id string) (*handler.AgentDeleteResp, error) {
	if seedAgent(id) {
		return nil, acl.NewError(acl.ClassRequest, "seed_agent_readonly",
			fmt.Sprintf("种子 agent %q 不可删除（可直接编辑 definitions）", id), nil)
	}
	s.mu.Lock()
	_, ok := s.agents[id]
	if !ok {
		s.mu.Unlock()
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	affected := s.conversationCountLocked(id)
	s.mu.Unlock()
	// 先摘活跃会话（有在途 turn 则整体拒绝，此时定义未删、文件未动）。
	detached := 0
	if s.orch != nil {
		n, err := s.orch.DetachAgent(id)
		if err != nil {
			return nil, err
		}
		detached = n
	}
	s.mu.Lock()
	delete(s.agents, id)
	dir := s.agentDir
	// 被删的正是「当前 Agent」的显式选择 → 清除之（回落内置种子），避免 default.json 悬空。
	wasDefault := s.currentAgent == id
	if wasDefault {
		s.currentAgent = ""
	}
	s.mu.Unlock()
	s.catalog.drop(id)        // 模型目录缓存与拒绝标记一并清掉（同 id 重建后从零取样）
	s.clearAgentLastError(id) // 最近失败快照清掉（同 id 重建后不回显旧错）
	if dir != "" {
		if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if wasDefault {
		clearDefaultAgentFile(dir)
		slog.Info("agents: 当前 Agent 被删除，回落内置默认", "deleted", id)
	}
	slog.Info("agent deleted", "agent", id, "affected_conversations", affected,
		"detached_conversations", detached, "was_default", wasDefault)
	return &handler.AgentDeleteResp{Deleted: id, AffectedConversations: affected,
		DetachedConversations: detached, WasDefault: wasDefault}, nil
}

// StartAgent 启动 agent 对应 runtime 连接（校验可启动：真实 Dial 成功才置 running）。
func (s *Service) StartAgent(ctx context.Context, id string) error {
	if s.orch == nil {
		return acl.NewError(acl.ClassConnection, "no_orchestrator", "编排器未装配（demo/纯列表模式不支持启停）", nil)
	}
	agent, ok := s.AgentByID(id)
	if !ok {
		return acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	spec, ok := s.reg.Get(agent.pickRuntime())
	if !ok {
		return acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", agent.pickRuntime()), nil)
	}
	if _, err := s.orch.acquire(ctx, agent, spec); err != nil {
		s.setAgentState(id, AgentStateStopped)
		// A3：启动失败时记录诊断（含握手/起进程阶段的 stderr 尾巴），供 /diagnostics 外露。
		s.recordAgentError(id, "", acl.Classify(err), acl.Classify(err).StderrTail)
		return err
	}
	s.setAgentState(id, AgentStateRunning)
	slog.Info("agent started", "agent", id, "runtime", spec.ID)
	return nil
}

// StopAgent 停止 agent 对应 runtime 进程：有在途 turn 时拒绝（先 cancel/等收尾）。
// 空闲/无连接时幂等置 stopped。
func (s *Service) StopAgent(ctx context.Context, id string) error {
	if s.orch == nil {
		return acl.NewError(acl.ClassConnection, "no_orchestrator", "编排器未装配（demo/纯列表模式不支持启停）", nil)
	}
	agent, ok := s.AgentByID(id)
	if !ok {
		return acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	spec, ok := s.reg.Get(agent.pickRuntime())
	if !ok {
		return acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", agent.pickRuntime()), nil)
	}
	return s.orch.stopAgent(ctx, agent, spec)
}

// persistAgentDef 原子写定义文件（先 tmp 后 rename）。
func (s *Service) persistAgentDef(d agentDefinitionFile) error {
	s.mu.Lock()
	dir := s.agentDir
	s.mu.Unlock()
	if dir == "" {
		return nil // 未启用 definitions 目录 → 仅内存（demo/测试）
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if len(d.Runtimes) == 0 {
		if a, ok := s.AgentByID(d.ID); ok {
			d.Runtimes = a.Runtimes
		}
	}
	if d.Name == "" {
		if a, ok := s.AgentByID(d.ID); ok {
			d.Name = a.Name
		}
	}
	if a, ok := s.AgentByID(d.ID); ok && d.Provider == "" {
		d.Provider = a.Provider
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+d.ID+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, d.ID+".json"))
}

// syncAgentDef 更新现有定义文件（存在则写；不存在则跳过——种子无文件场景）。
func (s *Service) syncAgentDef(id string) error {
	s.mu.Lock()
	dir := s.agentDir
	s.mu.Unlock()
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
		return nil // 无既有文件：种子元数据变更不落 definitions（默认配置即种子）
	}
	s.mu.Lock()
	cur, ok := s.agents[id]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.persistAgentDef(agentDefinitionFile{ID: id, Name: cur.Name, Runtimes: cur.Runtimes,
		Provider: cur.Provider, Mcps: cur.Mcps, RoutingPolicy: cur.RoutingPolicy,
		PermissionPolicy: cur.PermissionPolicy})
}

// agentRespLocked 取 agent 对外表示（须持锁外调用）。
func (s *Service) agentRespLocked(id string) *handler.AgentResp {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil
	}
	return &handler.AgentResp{ID: a.ID, Name: a.Name, Provider: a.Provider, State: a.State,
		IsDefault: a.ID == s.currentDefaultLocked(), Seed: seedAgent(a.ID), Runtimes: a.Runtimes,
		PermissionPolicy:  toPermissionPolicyResp(a.PermissionPolicy),
		ConversationCount: s.conversationCountLocked(a.ID)}
}
