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

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/acl"
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
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Runtimes []string `json:"runtimes"`           // 路由候选 runtime（P0 取首候选）
	Provider string   `json:"provider,omitempty"` // provider 档案 id（P2 provider 注入面）；可为空
	// 这里曾有 `Env map[string]string`（"预留：按 agent 附加 env，本轮不注入，仅持久化"）。
	// §5 复核后**删除**：它从未被读取/注入，唯一作用是诱使把密钥字面值停放在 agents/*.json 里
	// （明文落盘）。将来真要支持「按 agent 附加 env」，值必须支持 `vault:<ns>/<name>` 引用，
	// 而不是字面值——provider 档案已是这条路（见 providers.go）。
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
//
// **memory 模式（以及无持久层时的全部场景）走这条**；sqlite 模式走
// `LoadAgentDefinitionsFromStore`——两条路的差别只有"定义从哪读"，合并语义（种子铺底 +
// 非零字段覆盖）与 `seedAgent` 只读判定完全共用。
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
		if base == providersFileName || base == defaultAgentFile {
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

// LoadAgentDefinitionsFromStore 从持久层装载 agent 定义（sqlite 模式；M6-6）。
//
// 与 `LoadAgentDefinitions` 的三点差异，各自有理由：
//
//  1. **整体替换式**：先把内存里的非种子 agent 清掉，再逐行铺上——库里删掉的行不能以幽灵
//     agent 留在内存里（与能力面 `LoadCapabilityDefinitionsFromStore` 同源）。种子（dsh/opencode）
//     不落行，故这里是"清掉非种子 + 补齐种子"而不是清空整表；
//  2. **行里的 body 是 typed marshal**：解析成 `agentDefinitionFile` 后按 `applyDefinition(silent)`
//     合并，**非零字段才覆盖**——这正是"整行 update 会把种子字段抹空"那条坑的护栏（计划 §7 P-7）；
//  3. `dir` 仍然要设：`providers.json` 与 vault 锚点（`<AgentDir>` 的父目录）都还在文件侧
//     （计划 §7 P-9，目录不能被砍掉）。
func (s *Service) LoadAgentDefinitionsFromStore(ctx context.Context, dir string) int {
	return s.loadAgentDefinitionsFromStore(ctx, dir, slog.LevelInfo)
}

// ReloadAgentDefinitionsFromStore 重新装载 agent 定义（**网关每次取数前**调用；M7-2）。
//
// 与 `LoadAgentDefinitionsFromStore` 是同一份实现，只有汇总行的日志档位不同（理由同
// `ReloadCapabilityDefinitions`：网关是个长命进程，每次 tools/list/call 都会重装）。
func (s *Service) ReloadAgentDefinitionsFromStore(ctx context.Context, dir string) int {
	return s.loadAgentDefinitionsFromStore(ctx, dir, slog.LevelDebug)
}

// loadAgentDefinitionsFromStore 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (s *Service) loadAgentDefinitionsFromStore(ctx context.Context, dir string, level slog.Level) int {
	// 库读在锁外（别把一次 DB 读塞进临界区），但**清空 + 补齐种子 + 逐行铺上**必须在同一个
	// 临界区里完成：拆成两段就会留出"非种子 agent 全没了、行还没铺上"的窗口，读者（网关每次
	// 调用前都重装、核心侧并发请求）会看到"agent 不存在"——而库里的行一个字都没改。
	// M7-5 的并发用例就是这么撞上的。
	var rows model.AgentDefinitionList
	if s.st != nil {
		r, err := s.st.ListAgentDefinitions(ctx)
		if err != nil {
			s.defWarnOnce("agents:store", "agents: 从持久层读定义失败", "err", err)
			s.mu.Lock()
			s.agentDir = dir
			s.mu.Unlock()
			return 0
		}
		rows = r
	}

	s.mu.Lock()
	s.agentDir = dir
	for id := range s.agents {
		if !seedAgent(id) {
			delete(s.agents, id)
		}
	}
	s.seedBuiltinAgents() // 持锁调用（见函数注释：调用方持锁或处于构造期）
	loaded := 0
	for _, row := range rows {
		var d agentDefinitionFile
		if err := json.Unmarshal([]byte(row.Body), &d); err != nil {
			s.defWarnOnce("agents:json:"+row.ID, "agents: 定义行 JSON 非法，已跳过", "agent", row.ID, "err", err)
			continue
		}
		if d.ID != "" && d.ID != row.ID {
			// 行键是权威（它才是主键）；正文里的 id 只可能是历史遗留，静默采用会让
			// "删了 a 却仍在用 b"这种漂移无法察觉。
			s.defWarnOnce("agents:id:"+row.ID, "agents: 定义行 id 与正文 id 不一致，以行键为准", "row", row.ID, "body", d.ID)
		}
		d.ID = row.ID
		if err := s.applyDefinitionLocked(d, true); err != nil {
			s.defWarnOnce("agents:rej:"+row.ID, "agents: 定义行未采纳", "agent", row.ID, "err", err)
			continue
		}
		loaded++
	}
	s.mu.Unlock()

	if loaded > 0 {
		slog.Log(ctx, level, "agents: 定义已从持久层装载", "count", loaded)
	}
	// provider 档案与「当前 Agent」仍在文件/独立键上：前者是目录级配置（providers.json），
	// 后者落 meta（计划 §7 P-8）。
	s.loadProvidersFile(dir)
	s.loadDefaultAgentFromStore(ctx)
	return loaded
}

// seedBuiltinAgents 播种内置 Agent（dsh / opencode；仅当对应 runtime 已注册）。
// 幂等：调用方持锁或处于构造期。
func (s *Service) seedBuiltinAgents() {
	for _, b := range builtinAgents {
		if _, ok := s.reg.Get(b.runtime); !ok {
			continue
		}
		if _, exists := s.agents[b.id]; exists {
			continue
		}
		s.agents[b.id] = &AgentRec{
			ID:       b.id,
			Name:     b.name,
			Runtimes: []string{b.runtime},
			State:    AgentStateStopped,
		}
	}
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
	return s.applyDefinitionLocked(d, silent)
}

// applyDefinitionLocked applyDefinition 的持锁版本（调用方持 s.mu）：整体替换式装载要在**同一个
// 临界区**里清空 + 铺行，中途放开锁就会露出"agent 不存在"的窗口（见 loadAgentDefinitionsFromStore）。
func (s *Service) applyDefinitionLocked(d agentDefinitionFile, silent bool) error {
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
func (s *Service) CreateAgent(ctx context.Context, req dtoagent.AgentCreateReq) (*dtoagent.AgentResp, error) {
	if strings.HasPrefix(req.ID, reservedAgentPrefix) {
		return nil, acl.NewError(acl.ClassRequest, "reserved_agent_id", fmt.Sprintf("%q 为种子保留前缀，自定义 agent 请用其它 id", reservedAgentPrefix), nil)
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	d := agentDefinitionFile{ID: req.ID, Name: req.Name, Runtimes: req.Runtimes, Provider: req.Provider,
		Mcps: req.Mcps, PermissionPolicy: fromPermissionPolicyResp(req.PermissionPolicy)}
	if err := s.applyDefinition(d, false); err != nil {
		return nil, err
	}
	// 新建也要先过**定义文件里的完整字段**（Name 已回填），否则 DB 里会留下一行 name 为空的定义
	// ——它不出错，只是面板上显示成 id，属于"看不见的降级"。
	if d.Name == "" {
		d.Name = req.ID
	}
	if err := s.createAgentDefinition(ctx, d); err != nil {
		// 回滚内存登记
		s.mu.Lock()
		delete(s.agents, req.ID)
		s.mu.Unlock()
		return nil, err
	}
	return s.agentRespLocked(req.ID), nil
}

// UpdateAgent 更新 agent 元数据（name/runtimes/provider 子集；provider 传空串 = 清除）。
// 顺序是"先校验 → 再提交载体 → 最后改内存"：载体提交失败时内存一个字段都不动
// （M6 之前是"先改内存再写文件"，写失败会留下"内存里有、盘上没有"的半死态）。
func (s *Service) UpdateAgent(ctx context.Context, id string, patch dtoagent.AgentUpdateReq) (*dtoagent.AgentResp, error) {
	s.mu.Lock()
	cur, ok := s.agents[id]
	if !ok {
		s.mu.Unlock()
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	// 目标定义（纯计算；cur 只读，失败路径不留痕）。
	next := agentDefinitionFile{ID: id, Name: cur.Name, Runtimes: cur.Runtimes, Provider: cur.Provider,
		Mcps: cur.Mcps, RoutingPolicy: cur.RoutingPolicy, PermissionPolicy: cur.PermissionPolicy}
	// 通知判据要在**改内存之前**取值：`next` 由 `cur` 派生，下面会把新值写回同一个对象，
	// 改完再比就是拿新值跟新值比（通知永远不发）。
	beforeMcps := cur.Mcps
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
		next.Runtimes = *patch.Runtimes
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Provider != nil {
		next.Provider = *patch.Provider
	}
	if patch.PermissionPolicy != nil {
		pol := fromPermissionPolicyResp(patch.PermissionPolicy)
		if err := pol.Validate(); err != nil {
			s.mu.Unlock()
			return nil, acl.NewError(acl.ClassRequest, "invalid_permission_policy", err.Error(), nil)
		}
		next.PermissionPolicy = pol
	}
	// MCP 白名单三态（缺键不改 / null 清空 / [] 禁用全部 / 数组白名单）：解析失败是**请求形态**
	// 问题（40000），此刻内存与载体都还没动。
	mcps, mcpsPresent, err := decodeAgentMcpsPatch(patch.Mcps)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if mcpsPresent {
		next.Mcps = mcps
	}
	s.mu.Unlock()

	if err := s.updateAgentDefinition(ctx, next); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if after, ok := s.agents[id]; ok {
		after.Name = next.Name
		after.Runtimes = next.Runtimes
		after.Provider = next.Provider
		after.Mcps = next.Mcps
		if next.PermissionPolicy != nil {
			after.PermissionPolicy = next.PermissionPolicy
		}
	}
	s.mu.Unlock()
	// M6-8 / P-13：白名单真的变了才发通知——空闲会话的句柄作废，下一轮按当前定义重建；
	// 有在途轮次或未决权限/提问的顺延。载体已经提交成功，通知失败不改写响应（只落日志）。
	if agentMcpsChanged(beforeMcps, next.Mcps) {
		s.notifyAgentCapabilityChange(id, capabilityFaceFieldMcps)
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
func (s *Service) DeleteAgent(ctx context.Context, id string) (*dtoagent.AgentDeleteResp, error) {
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
	// 先删**载体**（CAS 失败 = 有人在这中间改过 → 报错，此时内存与显式选择都不动）。
	if _, err := s.removeAgentDefinition(ctx, id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	delete(s.agents, id)
	// 被删的正是「当前 Agent」的显式选择 → 清除之（回落内置种子），避免选择悬空。
	wasDefault := s.currentAgent == id
	if wasDefault {
		s.currentAgent = ""
	}
	s.mu.Unlock()
	s.catalog.drop(id)        // 模型目录缓存与拒绝标记一并清掉（同 id 重建后从零取样）
	s.clearAgentLastError(id) // 最近失败快照清掉（同 id 重建后不回显旧错）
	if wasDefault {
		s.clearDefaultAgent(ctx)
		slog.Info("agents: 当前 Agent 被删除，回落内置默认", "deleted", id)
	}
	slog.Info("agent deleted", "agent", id, "affected_conversations", affected,
		"detached_conversations", detached, "was_default", wasDefault)
	return &dtoagent.AgentDeleteResp{Deleted: id, AffectedConversations: affected,
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
	// 暖启动没有会话，因此没有能力选择 → 空指纹（缺省代际，与代际机制之前同键）。
	if _, err := s.orch.acquire(ctx, agent, spec, ""); err != nil {
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

// agentRespLocked 取 agent 对外表示（须持锁外调用）。
func (s *Service) agentRespLocked(id string) *dtoagent.AgentResp {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil
	}
	return &dtoagent.AgentResp{ID: a.ID, Name: a.Name, Provider: a.Provider, State: a.State,
		IsDefault: a.ID == s.currentDefaultLocked(), Seed: seedAgent(a.ID), Runtimes: a.Runtimes,
		Mcps: a.Mcps, PermissionPolicy: toPermissionPolicyResp(a.PermissionPolicy),
		ConversationCount: s.conversationCountLocked(a.ID)}
}
