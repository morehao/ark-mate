package manager

import (
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// ——— 能力集 → 指纹输入（§13.4 代际；`pkg/runtime.Fingerprint` 的唯一装配点） ———
//
// 指纹算法在 `pkg/runtime`（它要拿 `Projection`/`Spec` 的类型）；**"哪些内容进指纹"在这里决定**
// ——这里是唯一同时看得见能力定义（`pkg/capability`）与投影形态（`pkg/runtime`）的地方。
//
// 三条边界，每条都有对应的测试：
//
//  1. **密钥值绝不进指纹**：只用定义里的 `env`/`secret_env`/`secret_headers` 的**键名**与
//     引用名（`vault:`/`env:`），解析成明文的动作发生在注入前一刻（`resolveMcpSecrets`），
//     在那之前和之后都不参与指纹。指纹会进日志、事件载荷与 DB 列，进去就等于泄漏。
//  2. **正文摘要复用物化 sidecar 的同一算法**（`capability.PayloadHash`）：不另造一套摘要，
//     这样"指纹变了"与"物化摘要变了"永远同向，不会出现"指纹说没变、磁盘产物却该重写"。
//  3. **只纳入真正会生效的东西**：禁用的 skill、不投给该 runtime 的 skill、该 runtime 不支持的
//     传输的 MCP 都不进指纹——它们对这个进程的能力集没有任何影响，纳进来只会让"用户改了一个
//     无关字段"也换一次代际（白起一个进程）。

// projectedSkillParts 该 runtime 上**真正会投放**的 skill 列表（id + 正文摘要）。
//
// 门槛与物化完全一致：启用 + 声明投给该 runtime + 本进程解析出了该 runtime 的 skill 投影。
// 若该 runtime 根本没有 skill 投影（远程端点那样），技能不影响它，返回空。
func (s *Service) projectedSkillParts(runtimeID string) []runtime.SkillPart {
	if s.caps == nil {
		return nil
	}
	p, ok := s.skillProjectionFor(runtimeID)
	if !ok || p.Err != "" {
		return nil
	}
	var out []runtime.SkillPart
	for _, sk := range s.caps.ListSkills() {
		if !sk.IsEnabled() || !sk.AppliesToRuntime(runtimeID) {
			continue
		}
		out = append(out, runtime.SkillPart{ID: sk.ID, BodyDigest: capability.PayloadHash(sk.Body)})
	}
	return out
}

// mcpFingerprintParts 把 MCP 定义转成指纹片段（**只取键名与引用名**）。
//
// 输入一律是**定义**（`capability.McpServer` 的 `SecretEnv`/`SecretHeaders` 按类型只能是引用），
// 不是解析后的注入值——这一点由类型保证，不靠调用方记得剔除。
func mcpFingerprintParts(servers []capability.McpServer) []runtime.McpPart {
	out := make([]runtime.McpPart, 0, len(servers))
	for _, m := range servers {
		refs := make([]string, 0, len(m.SecretEnv)+len(m.SecretHeaders))
		for _, k := range capability.KeysOf(m.SecretEnv) {
			refs = append(refs, k+"="+m.SecretEnv[k])
		}
		for _, k := range capability.KeysOf(m.SecretHeaders) {
			refs = append(refs, k+"="+m.SecretHeaders[k])
		}
		out = append(out, runtime.McpPart{
			ID: m.ID, Transport: m.Transport,
			Command: m.Command, Args: append([]string(nil), m.Args...), URL: m.URL,
			// 明文的**键名**入指纹（`env` 的值不入：值可能带凭据，且换值不该换进程）。
			EnvKeys:    capability.KeysOf(m.Env),
			SecretRefs: refs,
		})
	}
	return out
}

// runtimeFingerprintPart 某 runtime 的形态片段（版本 + 绑定粒度 + 投影声明）。
func runtimeFingerprintPart(sp runtime.Spec) runtime.RuntimePart {
	return runtime.RuntimePart{
		ID: sp.ID, Version: sp.Version,
		Scope:       sp.CapabilityScope,
		Projections: append([]runtime.Projection(nil), sp.Projections...),
	}
}

// capabilityFingerprint 某 runtime 上「按 agent 声明解析出的整套能力」的指纹。
//
// **M1 只被测试与 M3 的显式能力选择调用**：缺省路径（会话没有声明能力选择）必须传
// `declared=false` 拿到空指纹，从而让 `connKey` 与今天逐字相同（R11 缺省零回归）。
// 这里刻意不提供"总是算一套"的入口——那正是会让默认路径悄悄换键的写法。
func (s *Service) capabilityFingerprint(agent *AgentRec, runtimeID string, declared bool) string {
	if !declared {
		return ""
	}
	in := runtime.FingerprintInput{Runtimes: []runtime.RuntimePart{{ID: runtimeID}}}
	if sp, ok := s.regSpec(runtimeID); ok {
		in.Runtimes[0] = runtimeFingerprintPart(sp)
	}
	in.Skills = s.projectedSkillParts(runtimeID)
	in.Mcps = mcpFingerprintParts(s.mcpSelectionFor(agent, runtimeID))
	return runtime.Fingerprint(in)
}

// regSpec 取已注册 runtime 的 Spec（ok=false = 未注册）。
func (s *Service) regSpec(runtimeID string) (runtime.Spec, bool) {
	if s.reg == nil {
		return runtime.Spec{}, false
	}
	return s.reg.Get(runtimeID)
}
