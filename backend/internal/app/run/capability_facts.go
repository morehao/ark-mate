package run

import (
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/domain/session"
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
func (s *Service) CapabilityFactsFor(runtimeID string, sel *session.CapabilitySelection) session.CapabilityFacts {
	facts := session.CapabilityFacts{}
	if spec, ok := s.regSpec(runtimeID); ok {
		facts.Spec, facts.SpecOK = spec, true
	}
	facts.Skills = s.projectedSkillsForFingerprint(runtimeID, sel)
	plan := s.planMcpInjection(runtimeID, sel)
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

// ——— 过渡期转发/别名（纪律 1：一行 + TODO；P4b 删） ———
//
// 选定的值对象（`session.CapabilitySelection`）连同这几个自由函数在 P3c 落 `domain/session`；本包还有
// ~40 处调用点（建会话的请求归一、代际裁决、读面回显、以及一大批集成用例），所以这一批保留
// 同名转发，调用点零改动。

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
