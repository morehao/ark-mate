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
		if filepath.Base(p) == "providers.json" {
			continue // provider 档案非同目录 agent 定义（见 loadProvidersFile）
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
		return nil
	}
	s.agents[d.ID] = &AgentRec{
		ID: d.ID, Name: d.Name, Runtimes: d.Runtimes, Provider: d.Provider, Mcps: d.Mcps, State: AgentStateStopped,
	}
	return nil
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
	d := agentDefinitionFile{ID: req.ID, Name: req.Name, Runtimes: req.Runtimes, Provider: req.Provider}
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
	s.mu.Unlock()
	if err := s.syncAgentDef(id); err != nil {
		return nil, err
	}
	return s.agentRespLocked(id), nil
}

// DeleteAgent 删除用户 agent（种子不可删）。已存在会话仍可查看（历史保留），
// 但其上新建 prompt 将报 agent_not_found。
func (s *Service) DeleteAgent(_ context.Context, id string) error {
	if strings.HasPrefix(id, reservedAgentPrefix) {
		return acl.NewError(acl.ClassRequest, "seed_agent_readonly", "种子 agent 不可删除（可直接编辑 definitions）", nil)
	}
	s.mu.Lock()
	_, ok := s.agents[id]
	if !ok {
		s.mu.Unlock()
		return acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	delete(s.agents, id)
	dir := s.agentDir
	s.mu.Unlock()
	if dir != "" {
		if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
	return s.persistAgentDef(agentDefinitionFile{ID: id, Name: cur.Name, Runtimes: cur.Runtimes, Provider: cur.Provider, Mcps: cur.Mcps})
}

// agentRespLocked 取 agent 对外表示（须持锁外调用）。
func (s *Service) agentRespLocked(id string) *handler.AgentResp {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil
	}
	return &handler.AgentResp{ID: a.ID, Name: a.Name, Provider: a.Provider, State: a.State, IsDefault: a.IsDefault()}
}
