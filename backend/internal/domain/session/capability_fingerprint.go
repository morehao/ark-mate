package session

import (
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 能力集 → 指纹输入（§13.4 代际；`pkg/runtime.Fingerprint` 的唯一装配点） ———
//
// 指纹算法在 `pkg/runtime`（它要拿 `Projection`/`Spec` 的类型）；**"哪些内容进指纹"在这里决定**
// ——这里是唯一同时看得见能力定义（`pkg/capability`）、投影形态（`pkg/runtime`）与会话钉住的
// 选定（`CapabilitySelection`）的地方。
//
// 三条边界（每条都有对应的测试，P3c 起随代码一起搬到本域，判据一字未改）：
//
//  1. **密钥值绝不进指纹**：只用定义里的 `env`/`secret_env`/`secret_headers` 的**键名**与
//     引用名（`store:`/`env:`），解析成明文的动作发生在注入前一刻，在那之前和之后都不参与指纹。
//     指纹会进日志、事件载荷与 DB 列，进去就等于泄漏。
//  2. **正文摘要复用物化 sidecar 的同一算法**（`capability.PayloadHash`）：不另造一套摘要，
//     这样"指纹变了"与"物化摘要变了"永远同向，不会出现"指纹说没变、磁盘产物却该重写"。
//  3. **只纳入真正会生效的东西**：禁用的 skill、不投给该 runtime 的 skill、该 runtime 不支持的
//     传输的 MCP 都不进指纹——它们对这个进程的能力集没有任何影响，纳进来只会让"用户改了一个
//     无关字段"也换一次代际（白起一个进程）。
//
// 事实由装配侧一次算好（`CapabilityFactsProvider`）：**解析**（读注册表、环境变量、网关开关、
// 拼网关条目）在那边，**放进指纹的取舍**在这里。这条分界让本域的单测可以喂平凡事实，
// 不必起真注册表。

// CapabilityFingerprint 某 runtime 上「按会话选定解析出的整套能力」的指纹。
//
// 事实经装配侧登记的 provider 现取（`SetCapabilityFacts`）；provider 未登记 → 只按 runtime id
// 算（与"runtime 未注册"同义：指纹里仍写 id，缺省路径照样返回空串）。
//
// **缺省路径（会话没有声明能力选择）必须拿到空指纹**：`sel == nil` 时直接返回 "",
// 从而让连接键与引入代际之前逐字相同（R11 缺省零回归）。这里刻意不提供"总是算一套"的入口
// ——那正是会让默认路径悄悄换键的写法。
//
// P3c：实现随 `capability_fingerprint.go` 从 manager 搬进本域，事实经 `CapabilityFacts` 传入。
// **判据与顺序一字未改**（`runtimeFingerprintPart` → skills → mcps → `runtime.Fingerprint`）。
func (pl *Plane) CapabilityFingerprint(runtimeID string, sel *CapabilitySelection) string {
	if sel == nil {
		return ""
	}
	facts := CapabilityFacts{}
	if pl.capFacts != nil {
		facts = pl.capFacts.CapabilityFactsFor(runtimeID, sel)
	}
	return FingerprintOf(facts, runtimeID, sel)
}

// FingerprintOf 纯函数形态的指纹装配（给定事实 → 指纹）。
//
// 与 `Plane.CapabilityFingerprint` 分开是为了让"取舍"可被单测直接喂事实验证：本域的单测不必
// 起真注册表，只要给一份 `CapabilityFacts`。
func FingerprintOf(facts CapabilityFacts, runtimeID string, sel *CapabilitySelection) string {
	if sel == nil {
		return ""
	}
	in := runtime.FingerprintInput{Runtimes: []runtime.RuntimePart{{ID: runtimeID}}}
	if facts.SpecOK {
		in.Runtimes[0] = RuntimeFingerprintPart(facts.Spec)
	}
	in.Skills = projectedSkillParts(facts, sel)
	// 事实里给的是**注入计划**：指纹拿它、注入也拿它（两处各算一遍必然漂移）。
	in.Mcps = mcpFingerprintParts(facts)
	return runtime.Fingerprint(in)
}

// projectedSkillParts 该 runtime 上**真正会投放**的 skill 列表（id + 正文摘要）。
//
// 门槛与物化完全一致：启用 + 声明投给该 runtime + 本进程解析出了该 runtime 的 skill 投影；
// 会话选定（M3）再收一道：`sel.Skills` 非 nil 时只取白名单里的（覆盖 skill 自己的 runtimes
// 声明——会话声明是用户的显式选择，求交会让白名单外的技能永远选不上且无法解释）。
//
// 事实侧（`facts.Skills`）已经过"启用 + 投给该 runtime + 有投影"三关；这里只做白名单那一道。
func projectedSkillParts(facts CapabilityFacts, sel *CapabilitySelection) []runtime.SkillPart {
	if len(facts.Skills) == 0 {
		return nil
	}
	var allow map[string]bool
	if sel != nil && sel.Skills != nil {
		allow = make(map[string]bool, len(*sel.Skills))
		for _, id := range *sel.Skills {
			allow[id] = true
		}
	}
	out := make([]runtime.SkillPart, 0, len(facts.Skills))
	for _, sk := range facts.Skills {
		if allow != nil && !allow[sk.ID] {
			continue
		}
		out = append(out, runtime.SkillPart{ID: sk.ID, BodyDigest: capability.PayloadHash(sk.Body)})
	}
	return out
}

// mcpFingerprintParts 把**注入计划**转成指纹片段（**只取键名与引用名**）。
//
// M7-3 的关键改动：输入从"定义集合"改成"注入计划"（`facts.Direct`/`facts.Gateway`）。两者的差别
// 就是网关的头号收益——远程 server 在计划里被折叠成**一条常量条目**，于是"加一个远程 server"
// 不再改变指纹、也就不换代际；而"加一个 stdio server"照旧改变指纹（行为不变）。
//
// 为什么不直接对注入条目的**产出**取指纹：那是**解析后的明文**，会让密钥值进指纹
// （§13.4 边界①）。计划这个值对象保住了同一件事——"指纹覆盖真正会被注入的东西"——同时把明文
// 留在解析那一刻。网关条目本身只有 scope（`Command` + `Args` + 稳定 name），天生无密钥，
// 故可以整体入指纹。
//
// 取舍（该注入哪些）已经由装配侧的计划算完了（含"显式空数组 = 一个都不要"那条短路），
// 域再判一次就是第二个事实源——所以这个函数**不收选择**。
func mcpFingerprintParts(facts CapabilityFacts) []runtime.McpPart {
	out := make([]runtime.McpPart, 0, len(facts.Direct)+1)
	for _, m := range facts.Direct {
		out = append(out, injectedMcpPart(m))
	}
	if gw := facts.Gateway; gw != nil {
		out = append(out, injectedMcpPart(*gw))
	}
	return out
}

// injectedMcpPart 单条注入条目 → 指纹片段（键名与引用名，绝不取值）。
//
// `SecretRefs` 由装配侧按 `键名=引用名` 逐条拼好（它才是看得见 `SecretEnv`/`SecretHeaders` 的那
// 一侧）；本域只搬运。拼法两处不一致的后果是**指纹悄悄换了**（同一套能力算出两个值 ⇒ 每轮换代际），
// 而两处都能编译、都"看起来对"——所以 `InjectedMcp.SecretRefs` 的契约写在类型注释里。
func injectedMcpPart(m InjectedMcp) runtime.McpPart {
	return runtime.McpPart{
		ID: m.ID, Transport: m.Transport,
		Command: m.Command, Args: append([]string(nil), m.Args...), URL: m.URL,
		// 明文的**键名**入指纹（`env` 的值不入：值可能带凭据，且换值不该换进程）。
		EnvKeys:    append([]string(nil), m.EnvKeys...),
		SecretRefs: append([]string(nil), m.SecretRefs...),
	}
}

// RuntimeFingerprintPart 某 runtime 的形态片段（版本 + 绑定粒度 + 投影声明）。
//
// 导出：它是"底座形态怎么进指纹"的唯一实现，读面（诊断）与指纹共用同一份投影声明。
func RuntimeFingerprintPart(sp runtime.Spec) runtime.RuntimePart {
	return runtime.RuntimePart{
		ID: sp.ID, Version: sp.Version,
		Scope:       sp.CapabilityScope,
		Projections: append([]runtime.Projection(nil), sp.Projections...),
	}
}
