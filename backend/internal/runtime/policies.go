package runtime

// ——— `Spec` 的行为开关收成策略接口（C3 4.7）———
//
// 改造前 `Spec` 里有七个**行为型**开关（`McpTransports`、`ManagesOwnContext`、`DeclaresModelInput`、
// `ModelCatalogNeedsCredential`、`CapabilityScope`+`Tested`+`TestedOnVersion`）与十三个纯数据字段
// 混在一张结构体里。两类东西的差别不是风格：
//
//   - 纯数据（`Caps` / `Isolation` / `ModelConfig` / `Credential` / `Projections` / `ModeMapping` /
//     `SetupSteps` / `Notes`）是**声明**，读它的人只做机械投影，加一个底座不用改内核；
//   - 行为开关是**回答"遇到某种情况怎么办"**。它们今天都还是常量，但语义上是"该底座会怎么表现"，
//     所以形状必须是**接口**：将来某个底座要现场探测（读二进制、探 `--version`、问 ACP initialize）
//     才能回答时，换实现即可，不用回头改 `Spec` 与所有消费点。
//
// 切分依据是**行为的归属**，不是开关的个数（七个开关只归三组）：
//
//	CapabilityPolicy   —— 能力面：谁能注入、上下文谁管、绑定粒度多细
//	ModelConfigPolicy  —— 模型清单写面：要不要写 input、目录是否按凭据过滤
//	CapabilityBinding  —— 绑定粒度的**实测记录**（数据：它是"上次实测结论"，不是行为）
//
// 计划里写的另外三个名字（`IsolationPlanner` / `PermissionMapper` / `Probe`）**今天没有对应的
// 行为开关**：`Isolation []IsolationVar`、`ModeMapping`、`SetupSteps`/`Notes` 已经是纯数据声明。
// 给它们套一层空接口只是仪式；等某一面真的长出"要现场判断"的行为再切（届时判据与反证在
// `docs/agents/guards.md`）。

// CapabilityBinding 能力绑定范围的**实测记录**。
//
// 三条纪律（P-2）落在 `EffectiveCapabilityScope`：① 未实测一律按 process；② 只有"同进程同 cwd、
// 新会话换能力集生效"才能声明 session，只有"同进程同 cwd 不生效、换 cwd 生效"才能声明 directory；
// ③ 绑定范围可能随底座版本变 ⇒ 记 `TestedOnVersion`，版本一变结论失效。
type CapabilityBinding struct {
	// Scope "process"（默认，最粗）| "directory" | "session"。
	Scope string
	// Tested 该 Scope 是否经真实底座实测。
	Tested bool
	// TestedOnVersion 实测时的底座版本。
	TestedOnVersion string
}

// CapabilityPolicy 该 runtime 的**能力面行为**（进程级事实，装配期问一次）。
type CapabilityPolicy interface {
	// McpTransports 支持的 MCP 传输（token：stdio|sse|http|streamable_http；空 = 仅 stdio）。
	McpTransports() []string
	// ManagesOwnContext 后端自管上下文/工具（§4.2）。
	ManagesOwnContext() bool
	// BindScope 能力面（MCP 集、skill 目录扫描）在哪个粒度被确定，以及它的实测记录。
	BindScope() CapabilityBinding
}

// ModelConfigPolicy 该 runtime **模型清单写面**的两个行为开关。
type ModelConfigPolicy interface {
	// DeclaresModelInput 平台生成的配置里是否给每个模型条目写 `input`（ADR-12）。
	DeclaresModelInput() bool
	// CatalogNeedsCredential 该底座的模型目录是否**按凭据过滤**（缺密钥 → 目录为空而不是报错）。
	CatalogNeedsCredential() bool
}

// —— 声明式实现 ——
//
// 今天在册的两处事实都是常量（`dsh` 与远程端点），差别只在取值；先给一个静态实现，接口留给
// "要现场探测才能回答"的底座。**这不是多余的一层**：消费点拿的是接口，加一个会探测的底座
// 不必改 `Spec`。

// StaticCapability 声明式的能力面策略。
type StaticCapability struct {
	Transports []string
	OwnContext bool
	Binding    CapabilityBinding
}

// McpTransports 支持的 MCP 传输（空 = 仅 stdio，由 `Registry.McpTransportsFor` 兜底）。
func (c StaticCapability) McpTransports() []string { return c.Transports }

// ManagesOwnContext 后端自管上下文。
func (c StaticCapability) ManagesOwnContext() bool { return c.OwnContext }

// BindScope 能力绑定范围与实测记录。
func (c StaticCapability) BindScope() CapabilityBinding { return c.Binding }

// StaticModelConfig 声明式的模型清单写面策略。
type StaticModelConfig struct {
	DeclaresInput        bool
	NeedsCredentialInCat bool
}

// DeclaresModelInput 平台是否写 `input`。
func (m StaticModelConfig) DeclaresModelInput() bool { return m.DeclaresInput }

// CatalogNeedsCredential 目录是否按凭据过滤。
func (m StaticModelConfig) CatalogNeedsCredential() bool { return m.NeedsCredentialInCat }

// —— Spec 上的读取口（nil 安全）——
//
// 未声明策略 = 最保守：不承载任何 MCP 传输（上层兜底成 stdio）、不自管上下文、绑定粒度 process、
// 不写 input、目录不按凭据过滤。零值保守是刻意的：**"忘了声明"不该等于"放开最细的粒度"**。

// McpTransports 支持的 MCP 传输（未声明 → nil，调用方兜底成 stdio）。
func (s Spec) McpTransports() []string {
	if s.Capability == nil {
		return nil
	}
	return s.Capability.McpTransports()
}

// ManagesOwnContext 后端自管上下文（未声明 → false）。
func (s Spec) ManagesOwnContext() bool {
	if s.Capability == nil {
		return false
	}
	return s.Capability.ManagesOwnContext()
}

// CapabilityBinding 能力绑定范围与实测记录（未声明 → 全零 = process + 未实测）。
func (s Spec) CapabilityBinding() CapabilityBinding {
	if s.Capability == nil {
		return CapabilityBinding{}
	}
	return s.Capability.BindScope()
}

// DeclaresModelInput 是否写 `input`（未声明 → false）。
func (s Spec) DeclaresModelInput() bool {
	if s.ModelConfigWriter == nil {
		return false
	}
	return s.ModelConfigWriter.DeclaresModelInput()
}

// CatalogNeedsCredential 目录是否按凭据过滤（未声明 → false）。
func (s Spec) CatalogNeedsCredential() bool {
	if s.ModelConfigWriter == nil {
		return false
	}
	return s.ModelConfigWriter.CatalogNeedsCredential()
}
