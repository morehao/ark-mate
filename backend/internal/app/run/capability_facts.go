package run

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— P3c：能力事实的装配适配器（域端口 `session.CapabilityFactsProvider` 的实现） ———
//
// 指纹的**装配点**在 `domain/session`（`session.CapabilityFingerprint`），但算它要读三样东西：
// runtime 注册表、skill 投影面、MCP 注入计划（后者会拼网关条目——要拿本进程自己的可执行文件
// 路径）。那三样都只有装配侧够得着，所以这里把它们算成**平凡事实**交下去。
//
// 这一层是纯适配：**不做任何取舍判断**（哪些 skill 会投放、哪些 MCP 会注入，都在下面那几个
// 已有实现里，域只是消费方）。

// CapabilityFactsFor 实现 `session.CapabilityFactsProvider`。
//
// 算指纹时**刻意不传会话 id**（`planMcpInjection(runtimeID, "", sel)`）：会话 id 只落在网关
// 条目的 `env` **取值**里，而指纹只认 `env` 的**键名**（`EnvKeys`）——值根本不参与，传了也只是
// 噪音。更硬的一条：`session/new` 之前那个 id 还不一定存在（`StartSession` 先算指纹、后铸 id），
// 于是"指纹那一路能不能看见它"会变成随调用顺序而变的事。
func (s *Service) CapabilityFactsFor(runtimeID string, sel *session.CapabilitySelection) session.CapabilityFacts {
	facts := session.CapabilityFacts{}
	if spec, ok := s.regSpec(runtimeID); ok {
		facts.Spec, facts.SpecOK = spec, true
	}
	facts.Skills = s.projectedSkillsForFingerprint(runtimeID, sel)
	plan := s.planMcpInjection(runtimeID, "", sel)
	facts.Direct = injectedMcpsOfServers(plan.Direct)
	facts.Remote = injectedMcpsOfServers(plan.Remote)
	if plan.Gateway != nil {
		gw := injectedMcpOfRuntime(*plan.Gateway)
		facts.Gateway = &gw
	}
	return facts
}

// projectedSkillsForFingerprint 该 runtime 上**真正会投放**的 skill 事实。
//
// 门槛与物化完全一致：启用 + 声明投给该 runtime + 本进程解析出了该 runtime 的 skill 投影。
// **会话选定的白名单那一道不在这里**（它在域的 `projectedSkillParts`）：三态语义
// （未声明 / 空数组 / 白名单）只许有一个解释处。
func (s *Service) projectedSkillsForFingerprint(runtimeID string, _ *session.CapabilitySelection) []capability.Skill {
	if s.facts.Capabilities == nil {
		return nil
	}
	p, ok := s.facts.Skills.Projections().For(runtimeID)
	if !ok || p.Err != "" {
		return nil
	}
	var out []capability.Skill
	for _, sk := range s.facts.Capabilities.ListSkills() {
		if !sk.IsEnabled() || !sk.AppliesToRuntime(runtimeID) {
			continue
		}
		out = append(out, sk)
	}
	return out
}

// ——— 读面事实装配（P3c 的收尾形态） ———
//
// 这几个函数只做一件事：把注册表 / 定义 / 网关条目折成域侧事实值（`session.InjectedMcp`、
// `runtime.Spec`）。它们只有"事实怎么取"的知识，没有判决——谁能注入、指纹怎么算都在
// `domain/session`。
//
// 这批函数在 P3c 曾以"过渡期同名转发"的形式留下（原注释写着 P4b 删）。收口后它们已经是
// **唯一实现**、没有转发目标可删，那段"临时"口径不成立，故改成本段的形态说明。

// regSpec 取已注册 runtime 的 Spec（ok=false = 未注册）。
//
// P3c：随指纹装配点从 `capability_fingerprint.go` 搬到这里——调用方（MCP 注入计划、能力事实
// 适配器）都还在本包，而"注册表里有没有这个 runtime"是同一件事。
func (s *Service) regSpec(runtimeID string) (runtime.Spec, bool) {
	if s.reg == nil {
		return runtime.Spec{}, false
	}
	return s.reg.Get(runtimeID)
}

// injectedMcpsOfServers 把定义列表转成域侧的事实值对象（**只带键名与引用名**）。
func injectedMcpsOfServers(servers []capability.McpServer) []session.InjectedMcp {
	if len(servers) == 0 {
		return nil
	}
	out := make([]session.InjectedMcp, 0, len(servers))
	for _, m := range servers {
		out = append(out, injectedMcpOfServer(m))
	}
	return out
}

// injectedMcpOfServer 单条定义 → 域侧事实。
func injectedMcpOfServer(m capability.McpServer) session.InjectedMcp {
	return session.InjectedMcp{
		ID: m.ID, Transport: m.Transport,
		Command: m.Command, Args: append([]string(nil), m.Args...), URL: m.URL,
		EnvKeys:    capability.KeysOf(m.Env),
		SecretRefs: secretRefPairs(m.SecretEnv, m.SecretHeaders),
	}
}

// injectedMcpOfRuntime 网关条目（`runtime.McpServer`）→ 域侧事实。
//
// 网关条目天生无密钥（只有 scope），所以没有 `SecretRefs` 那一列——与直连条目同一份事实形状。
func injectedMcpOfRuntime(m runtime.McpServer) session.InjectedMcp {
	return session.InjectedMcp{
		ID: m.Name, Transport: m.Transport,
		Command: m.Command, Args: append([]string(nil), m.Args...), URL: m.URL,
		EnvKeys: capability.KeysOf(m.Env),
	}
}

// secretRefPairs 把密钥型 env/header 摊成 `键名=引用名` 的稳定列表（**绝不取值**）。
//
// 顺序由 `capability.KeysOf` 给（它内部排序）：指纹要求确定性，map 迭代顺序不能出现在这里。
func secretRefPairs(secretEnv, secretHeaders map[string]string) []string {
	out := make([]string, 0, len(secretEnv)+len(secretHeaders))
	for _, k := range capability.KeysOf(secretEnv) {
		out = append(out, k+"="+secretEnv[k])
	}
	for _, k := range capability.KeysOf(secretHeaders) {
		out = append(out, k+"="+secretHeaders[k])
	}
	return out
}
