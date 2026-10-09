package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/internal/vault"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
)

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐 LoadAgentDefinitions 容错）。
//
// **语义降级（M6-2，必须写在注释里，避免后人误当事实源）**：自 v8 起，sqlite 模式下本方法
// **不再是装载源**——能力定义的事实源是 DB（`skills`/`mcp_servers`），本方法只剩两个用途：
//  1. **memory 模式**（`FORGE_STORE=memory`，或无 DB 文件时的 auto）的装载路径：memory 模式
//     没有持久层，文件是它唯一的持久形态，不给它造第二套存储、也不让能力静默变空表；
//  2. **导入/导出路径**（M6-4 的 `forge capability import|export`）：`LoadDir` 降级为
//     "把存量文件读成定义"的编解码入口。
//
// 登记 `capabilityDir` 的语义也随之降级为"导入/导出目录"（见 service.capabilityDir）。
func (s *Service) LoadCapabilityDefinitions(dir string) int {
	if s.caps == nil {
		return 0
	}
	// 登记目录：导入/导出与（M6-3 之前的）启停写回都按它定位（没有它就只能内存生效，报错给出路）。
	s.mu.Lock()
	s.capabilityDir = dir
	s.mu.Unlock()
	return s.caps.LoadDir(dir).Total()
}

// LoadCapabilityDefinitionsFromStore 从持久层装载能力定义（sqlite 模式的事实源，M6-2）。
//
// 三步：**清空 → 逐行解码 → 过注册闸门**。顺序不能换：
//   - 先 Reset 是"整体替换"语义的前提（否则库里删掉的行会以幽灵定义留下，见 Registry.Reset）；
//   - 过 `RegisterSkill`/`RegisterMcpServer` 闸门是必须的：定义进 DB 的**写面**要走同一道闸门，
//     但 DB 是可达的（`forge capability import`、将来的 SQL 手工修库），装载是最后一道防线——
//     一手坏定义不该让整个能力面起不来。
//
// 坏行/非法定义**逐条 WARN 跳过、不阻断启动**，与 `LoadDir` 的容错口径逐字一致（用户目录里
// 一个坏文件从来不阻挡服务启动，DB 化不该把这条变严）。返回成功装载的条数。
//
// `importDir` 只用于登记"导入/导出目录"（能力定义仍在文件里的那份存量）；空串 = 不登记。
func (s *Service) LoadCapabilityDefinitionsFromStore(ctx context.Context, importDir string) int {
	if s.caps == nil || s.st == nil {
		return 0
	}
	if importDir != "" {
		s.mu.Lock()
		s.capabilityDir = importDir
		s.mu.Unlock()
	}
	s.caps.Reset()
	total := 0

	skills, err := s.st.ListSkillDefinitions(ctx)
	if err != nil {
		slog.Warn("capability: 从持久层读 skill 定义失败（能力面将为空）", "err", err)
		return total
	}
	for i := range skills {
		row := skills[i]
		var sk capability.Skill
		if err := json.Unmarshal([]byte(row.Body), &sk); err != nil {
			slog.Warn("capability: skill 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		// id 以**行主键**为准：body 里的 id 与行主键不一致时，行主键是事实（它是 CAS 与审计的键）。
		// 这与文件路径的"文件名兜底"同一思路——文件名/主键是可寻址的那一个。
		if sk.ID != row.ID {
			if sk.ID != "" {
				slog.Warn("capability: skill 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", sk.ID)
			}
			sk.ID = row.ID
		}
		// SourceFile 刻意**不填**：DB 化之后"文件来源"不再成立（计划 §9 的风险项），
		// 库视图的来源口径在 M6-5/M6-6 统一重定义为 db / builtin / imported。
		if err := s.caps.RegisterSkill(&sk); err != nil {
			slog.Warn("capability: skill 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	mcps, err := s.st.ListMcpServerDefinitions(ctx)
	if err != nil {
		slog.Warn("capability: 从持久层读 mcp server 定义失败", "err", err)
		return total
	}
	for i := range mcps {
		row := mcps[i]
		var m capability.McpServer
		if err := json.Unmarshal([]byte(row.Body), &m); err != nil {
			slog.Warn("capability: mcp server 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		if m.ID != row.ID {
			if m.ID != "" {
				slog.Warn("capability: mcp 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", m.ID)
			}
			m.ID = row.ID
		}
		if err := s.caps.RegisterMcpServer(&m); err != nil {
			slog.Warn("capability: mcp 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	slog.Info("capability: 定义已从持久层装载", "skills", len(skills), "mcps", len(mcps), "adopted", total)
	return total
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / P2 MCP live）。
func (s *Service) RegisterTool(t capability.Tool) error {
	return s.caps.RegisterTool(&t)
}

// ListSkills 返回 Skill 目录（按 ID 排序）。
func (s *Service) ListSkills() ([]dtocapability.SkillResp, error) {
	items := s.caps.ListSkills()
	out := make([]dtocapability.SkillResp, 0, len(items))
	for _, sk := range items {
		out = append(out, dtocapability.SkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body,
			Runtimes: append([]string(nil), sk.Runtimes...), Scope: sk.EffectiveScope(),
			Enabled: sk.IsEnabled(), Warnings: skillWarnings(sk),
		})
	}
	return out, nil
}

// ListMcpServers 返回 MCP 注册表（按 ID 排序）。headers/凭证只存内部供 P2 注入，不读回。
// 附 live 探测状态（tool_count / last_probe_at / last_probe_error，P2 §5.2）。
func (s *Service) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	counts := s.mcpToolCounts()
	probes := s.mcpProbeSnapshot()
	out := make([]dtocapability.McpServerResp, 0, len(items))
	for _, m := range items {
		resp := dtocapability.McpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Scope: m.EffectiveScope(), Enabled: m.IsEnabled(),
			EnvKeys:              capability.KeysOf(m.Env),
			SecretEnvKeys:        capability.KeysOf(m.SecretEnv),
			SecretHeaderKeys:     capability.KeysOf(m.SecretHeaders),
			UnresolvedSecretKeys: s.unresolvedSecretKeys(m),
			ToolCount:            counts[m.ID],
		}
		if st, ok := probes[m.ID]; ok {
			if !st.At.IsZero() {
				resp.LastProbeAt = st.At.UTC().Format(time.RFC3339)
			}
			resp.LastProbeErr = st.Err
		}
		out = append(out, resp)
	}
	return out, nil
}

// ListTools 返回 Tool 目录（按 name 排序）。
func (s *Service) ListTools() ([]dtocapability.ToolResp, error) {
	items := s.caps.ListTools()
	out := make([]dtocapability.ToolResp, 0, len(items))
	for _, t := range items {
		out = append(out, dtocapability.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}
	return out, nil
}

// supportedMcpTransports 返回 runtime 声明支持的 MCP 传输（未声明 → 仅 stdio，ACP 全 agent 必须支持）。
// 未知 runtime → ok=false。
func (s *Service) supportedMcpTransports(runtimeID string) ([]string, bool) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil, false
	}
	if len(spec.McpTransports) == 0 {
		return []string{capability.TransportStdio}, true
	}
	return spec.McpTransports, true
}

// AgentCapabilities 组装某 Agent 的统一能力目录（§8.1；可见性规则见 dtocapability.AgentCapabilitiesResp）。
// 只读聚合：不触发物化/注入/探测，全部依据注册表 + 候选 runtime 声明推导。未知 agent → agent_not_found。
func (s *Service) AgentCapabilities(agentID string) (*dtocapability.AgentCapabilitiesResp, error) {
	a, ok := s.AgentByID(agentID)
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("未知 agent %q", agentID), nil))
	}
	runtimes := append([]string(nil), a.Runtimes...)
	inAgent := make(map[string]bool, len(runtimes))
	for _, r := range runtimes {
		inAgent[r] = true
	}
	notes := make([]string, 0, 2)
	if len(runtimes) == 0 {
		notes = append(notes, "该 agent 未声明候选 runtime，能力目录为空")
	}
	// agent 白名单语义：nil = 未声明（全启用）；非 nil = 命中制（空数组 = 禁用 MCP 注入）。
	enabled := func(id string) bool {
		if a.Mcps == nil {
			return true
		}
		for _, x := range *a.Mcps {
			if x == id {
				return true
			}
		}
		return false
	}
	if a.Mcps != nil && len(*a.Mcps) == 0 {
		notes = append(notes, "该 agent 声明 mcps: []（禁用 MCP 注入）")
	}

	// 候选 runtime → 支持的 MCP 传输（未知 runtime 记降级说明，不臆造能力）。
	transports := make(map[string][]string, len(runtimes))
	for _, r := range runtimes {
		ts, known := s.supportedMcpTransports(r)
		if !known {
			notes = append(notes, fmt.Sprintf("候选 runtime %q 未注册（按声明过滤，不推导其能力）", r))
			continue
		}
		transports[r] = ts
	}

	// MCP：注册表全量 + 每条的 enabled/runtimes（候选里支持该传输的子集，保持候选声明顺序）。
	servers := s.caps.ListMcpServers()
	mcpOut := make([]dtocapability.AgentMcpServerResp, 0, len(servers))
	carried := make(map[string]bool, len(servers)) // server id → 至少一个候选可承载
	for _, m := range servers {
		carriers := make([]string, 0, len(runtimes))
		for _, r := range runtimes {
			for _, t := range transports[r] {
				if t == m.Transport {
					carriers = append(carriers, r)
					break
				}
			}
		}
		if len(carriers) > 0 {
			carried[m.ID] = true
		}
		mcpOut = append(mcpOut, dtocapability.AgentMcpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Enabled: enabled(m.ID), Runtimes: carriers,
		})
	}

	// Skill：全量列出 + 每条给「启用/适用」两个正交判断（§13.4 S5）。
	// carrierRuntimes 是**本 agent 视角下的实际投放目标**（候选 ∩ 技能声明 ∩ 已解析投影），
	// 空 + reasons 就是「这个 agent 用不上」。
	projected := s.projectedRuntimeSet()
	skills := s.caps.ListSkills()
	skillOut := make([]dtocapability.AgentSkillResp, 0, len(skills))
	for _, sk := range skills {
		carriers := make([]string, 0, len(runtimes))
		for _, rt := range runtimes {
			if inAgent[rt] && sk.AppliesToRuntime(rt) && projected[rt] {
				carriers = append(carriers, rt)
			}
		}
		act := SkillActivationFor(sk, runtimes, inAgent, projected)
		skillOut = append(skillOut, dtocapability.AgentSkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Runtimes: carriers,
			Enabled: sk.IsEnabled(), Applicable: act.Applicable, Reasons: act.Reasons,
		})
	}

	// Tool：builtin/observed 恒可见；mcp 条目须归属 server 对该 agent 启用且至少一个候选可承载。
	tools := s.caps.ListTools()
	toolOut := make([]dtocapability.ToolResp, 0, len(tools))
	for _, t := range tools {
		if t.Source == capability.ToolSourceMCP && t.McpServer != "" {
			if !enabled(t.McpServer) || !carried[t.McpServer] {
				continue
			}
		}
		toolOut = append(toolOut, dtocapability.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}

	return &dtocapability.AgentCapabilitiesResp{
		AgentID: a.ID, Name: a.Name, Runtimes: runtimes,
		Skills: skillOut, McpServers: mcpOut, Tools: toolOut, Notes: notes,
	}, nil
}

// unresolvedSecretKeys 列出"绑了引用但当前解析不出来"的密钥键（只读体检，不解密出值）。
//
// vault 引用用 `Store.Has`（只看密文文件在不在，不解密）——列表端点不该为了体检去解密，
// 而且"密文在但解不开"（vault.key 换了）属于另一个更严重的问题，会在注入时报出来。
func (s *Service) unresolvedSecretKeys(m capability.McpServer) []string {
	var out []string
	for _, k := range capability.KeysOf(m.SecretEnv) {
		if !s.secretRefResolvable(m.SecretEnv[k]) {
			out = append(out, k)
		}
	}
	for _, k := range capability.KeysOf(m.SecretHeaders) {
		if !s.secretRefResolvable(m.SecretHeaders[k]) {
			out = append(out, k)
		}
	}
	return out
}

// secretRefResolvable 报告一条引用当前是否"看起来能解析"（词法已在校验期保证，这里只看存在性）。
func (s *Service) secretRefResolvable(ref string) bool {
	switch capability.SecretRefKind(ref) {
	case "vault":
		st, err := s.providerVault()
		if err != nil || st == nil {
			return false
		}
		return st.Has(ref)
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		return os.Getenv(name) != ""
	default:
		return false
	}
}

// resolveMcpSecrets 把 server 的明文绑定与密钥引用合成**最终注入值**：
//
//	Env（明文）+ SecretEnv（vault:/env: 引用解析后）→ 环境变量
//	Headers（明文）+ SecretHeaders（解析后）        → header
//
// 任一引用解析失败 → 返回全部失败原因（调用方据此跳过注入 + 告警）。失败原因**只含键名与引用**，
// 绝不含密钥值本身——日志不该成为泄漏面。
func (s *Service) resolveMcpSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	env = make(map[string]string, len(m.Env)+len(m.SecretEnv))
	headers = make(map[string]string, len(m.Headers)+len(m.SecretHeaders))
	for k, v := range m.Env {
		env[k] = v
	}
	for k, v := range m.Headers {
		headers[k] = v
	}
	apply := func(field string, bindings map[string]string, dst map[string]string) {
		for _, k := range capability.KeysOf(bindings) {
			ref := bindings[k]
			val, err := s.resolveSecretRef(ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s[%q] 引用 %q 无法解析：%v", field, k, ref, err))
				continue
			}
			dst[k] = val
		}
	}
	apply("secret_env", m.SecretEnv, env)
	apply("secret_headers", m.SecretHeaders, headers)
	return env, headers, problems
}

// resolveSecretRef 解析一条密钥引用：
//
//	vault:<ns>/<name>  → vault 密文解密（AAD 带 kind=mcp_secret，与 provider 密钥不通用）
//	env:<NAME>         → 进程环境变量（容器/CI 注入；盘上不留痕）
//
// vault 不可用（未配置 agentDir / 密钥丢失）不是"静默跳过"，而是明确失败——理由同 §5：
// 让 MCP server 带着空密钥跑起来，问题会以"tools/list 返回 401"的面目出现在很远的地方。
func (s *Service) resolveSecretRef(ref string) (string, error) {
	if err := capability.ValidateSecretRef(ref); err != nil {
		return "", err
	}
	switch capability.SecretRefKind(ref) {
	case "vault":
		st, err := s.providerVault()
		if err != nil {
			return "", fmt.Errorf("vault 不可用：%w", err)
		}
		if st == nil {
			return "", fmt.Errorf("vault 不可用：本进程未配置能力/agent 目录（无 ~/.forge 可放密文）")
		}
		b, err := st.Get(ref, vault.KindMcpSecret)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		if v := os.Getenv(name); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("环境变量 %s 未设置或为空", name)
	default:
		return "", fmt.Errorf("不支持的引用 %q", ref)
	}
}

// mcpSelectionFor 选出该 agent×runtime 的 MCP **候选定义**（未解析密钥）。
//
// 这是「哪些 server 属于这套能力」的**唯一**实现：注入面（McpServersFor）与代际指纹
// （capabilityFingerprint）都从这里取。两处各写一遍的后果是"指纹里有的、注入时没有"（或反之）——
// 后者更糟：指纹说这套能力包含某个 server，实际却没注入。
//
// 与注入面的差别只有一处、且是**有意**的：这里不解析密钥（能不能解析是环境事实，不该让指纹抖动）。
//
// **白名单优先级（M3）**：会话声明了 `capabilities.mcps` → 以它为准（M3-2 的落点）；
// 会话没声明该 kind → 回落 `agent.Mcps`；两者都未声明 → 全量按 transport 过滤（今天的行为）。
// 会话级的选定**覆盖**而不是求交：agent 声明是"这个 agent 默认能碰什么"，会话声明是用户此刻
// 的显式选择，求交会让"agent 白名单里没有的 server"永远选不上，用户看不到任何解释。
func (s *Service) mcpSelectionFor(agent *AgentRec, runtimeID string, sel *CapabilitySelection) []capability.McpServer {
	if s.caps == nil {
		return nil
	}
	supported, ok := s.supportedMcpTransports(runtimeID)
	if !ok {
		return nil
	}
	want := make(map[string]bool, len(supported))
	for _, t := range supported {
		want[t] = true
	}
	allowIDs := (*[]string)(nil)
	if sel != nil && sel.Mcps != nil {
		allowIDs = sel.Mcps // 会话选定优先（含空数组 = 本会话一个 MCP 都不要）
	} else if agent != nil {
		allowIDs = agent.Mcps
	}
	var allow map[string]bool
	if allowIDs != nil {
		allow = make(map[string]bool, len(*allowIDs))
		for _, id := range *allowIDs {
			allow[id] = true
		}
	}
	servers := s.caps.ListMcpServers()
	out := make([]capability.McpServer, 0, len(servers))
	for _, m := range servers {
		if !m.IsEnabled() {
			continue // 禁用 = 不注入（overlay：定义/密钥引用/探测结果都保留，只停止注入）
		}
		if !want[m.Transport] {
			continue
		}
		if allow != nil && !allow[m.ID] {
			continue // agent 白名单未启用该 server（含空白名单 = 全部跳过）
		}
		out = append(out, m)
	}
	return out
}

// McpServersFor 选出可注入某 agent×runtime 的 MCP server 子集（P2 §5.2/§5.3）：
// 注册表全量 → 该 runtime 的 McpTransports 过滤 → 白名单（会话选定 > agent.Mcps；
// 两者都未声明 = 全量；空数组 = 一个都不要）。headers/凭证一并携带（走 ACP wire）。
// 未知 runtime → nil（维持现状不下发）。
//
// `sel` 是**会话钉住的能力选定**：新建会话时来自本次请求，恢复会话时来自
// `conversations.capability_json`。两处调用点都必须传它——恢复路径漏传的后果是
// 会话重启后注入集悄悄变回 agent 默认（用户看不到任何提示）。
func (s *Service) McpServersFor(agent *AgentRec, runtimeID string, sel *CapabilitySelection) []acl.McpServer {
	servers := s.mcpSelectionFor(agent, runtimeID, sel)
	out := make([]acl.McpServer, 0, len(servers))
	for _, m := range servers {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		// §13.3 M1/M6：密钥型绑定在这里**才**解析成明文（定义文件里只有引用）。
		env, headers, problems := s.resolveMcpSecrets(m)
		if len(problems) > 0 {
			// 解析不了就不注入这个 server：半配置的 MCP server 会让 agent 侧报一堆看不懂的连不上，
			// 而少一个工具是"看得见的降级"（日志里有原因、有引用、有出路）。**不静默**：逐条 WARN。
			slog.Warn("mcp 注入跳过：密钥引用解析失败", "server", m.ID, "problems", problems)
			continue
		}
		out = append(out, acl.McpServer{
			Name: name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Env: env, Headers: headers,
		})
	}
	return out
}

// projectedRuntimeSet 本进程已解析出 skill 投影的 runtime 集合（激活判定与投放集共用）。
func (s *Service) projectedRuntimeSet() map[string]bool {
	ps := s.skillProjectionsSnapshot()
	out := make(map[string]bool, len(ps))
	for _, p := range ps {
		if p.Err == "" {
			out[p.Runtime] = true
		}
	}
	return out
}

// skillWarnings 把注册表的装载期告警转成 REST 读模型（两处共用一个转换，免漂移）。
func skillWarnings(sk capability.Skill) []dtocapability.SkillWarningResp {
	out := make([]dtocapability.SkillWarningResp, 0, len(sk.Warnings))
	for _, w := range sk.Warnings {
		out = append(out, dtocapability.SkillWarningResp{
			Severity: w.Severity.String(), Pattern: w.Pattern, Message: w.Message,
		})
	}
	return out
}
