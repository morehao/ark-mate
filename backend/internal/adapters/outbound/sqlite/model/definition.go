package model

// ——— 定义/资源的写面列形状（v3.1 P4 从 `internal/adapters/outbound/sqlite/store` 上移）———
//
// 为什么上移：`domain/{provider,automation}` 通过窄端口读写这些结构，而它们不该认识 store 门面。
// 字段与 JSON 形状一个未改。

// AutomationRuntimeState 一条定时任务的运行态（**列**；`state` 不在其中——它属于定义，见 P-10）。
//
// 时间一律 **RFC3339Nano(UTC) 文本**（空串 = 未触发过）：裸 SQL 入参不经过 serializer，
// 这个结构体就是"运行态列"的边界，字符串化在 manager 完成、在这里原样落库。
type AutomationRuntimeState struct {
	// LastFiredAt 最近一次被认领的 occurrence（空 = 从未触发）。
	LastFiredAt string
	// LastStatus 最近一次结果（completed/failed/interrupted/skipped）。
	LastStatus string
	// LastReason 最近一次没跑成/失败的原因（人话）。
	LastReason string
	// LastSessionID 最近一次触发产生的会话 id。
	LastSessionID string
	// ConsecutiveFailures 连续失败次数。
	ConsecutiveFailures int64
	// RecentRuns 最近若干次触发的留痕（JSON 数组文本；空 = 不改这一列）。
	//
	// 空 = 不改（而不是"清空"）：调用方只推进 `last_fired_at` 时不该顺手把运行史抹掉。
	RecentRuns string
}

// AutomationOverlay 一次运行态写回（可能同时改定义里的 `state`，见 P-10 的说明）。
type AutomationOverlay struct {
	// Body 非空 = 同时覆盖定义正文（平台写 `state` 这类**非保留键**时用）。
	Body string
	// ExpectedRev 仅当 Body 非空时有意义（body 的 CAS 判据）。
	ExpectedRev int64
	// ExpectedLastFired 认领 CAS 判据（"这个 occurrence 还没被别人认领"）。
	ExpectedLastFired string
	// Runtime 要写入的运行态列。
	Runtime AutomationRuntimeState
}

// ProviderColumns 是 provider 行的**内容列**（身份列是 `ID`，记账列由 CAS 语义决定）。
//
// 它就是 `providerBodyJSON` 的替代物：列化之后不再有"未知键原样带走"这回事——键集合是封闭的，
// 于是"写了、存了、什么都不发生"的字段结构上进不来（设计 P7）。
type ProviderColumns struct {
	ID       string
	Name     string
	Protocol string
	BaseURL  string
	Note     string
}

// DefinitionImportItem 一条待导入的定义（调用方已逐条过闸门）。
type DefinitionImportItem struct {
	// Kind 见 model.DefinitionKindSkill / DefinitionKindMcp / DefinitionKindAutomation /
	// DefinitionKindProvider / DefinitionKindModel。
	Kind string
	ID   string
	// Body 定义正文（承载未知键的完整 JSON）。
	//
	// **kind=provider 时不用它**：v13 起 provider 是资源表（列 + 无 body），列载荷走 Provider。
	// 留着这个字段是给四个「定义载体」表用的（它们的正文是用户自由文档）。
	Body string
	// Provider 仅 kind=provider 时有意义：列化后的内容列。
	//
	// 单独一个字段而不是把列塞进 `Body`：Body 是"正文 JSON"，而 provider 的键集合封闭且有
	// 库级约束（`CHECK (protocol IN ('openai'))`）——把列值编码回 JSON 再在 store 里解回来，
	// 只是把已经消失的 body 重新造一遍。
	Provider *ProviderColumns
	// AutomationRuntime 仅 kind=automation 时有意义：定时任务的运行态在 DB 载体里是**列**，
	// 而存量文件里它是同文件的一部分（`last_fired_at`/`recent_runs`/…）。导入必须把两部分
	// **同一事务**写进去，否则会出现"定义进来了、运行史没进来"的半截状态。
	// nil = 该定义没有任何运行态（未触发过），落成默认列值。
	AutomationRuntime *AutomationRuntimeState
}
