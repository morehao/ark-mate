package model

// 本文件是**持久层内部**使用的具名常量，用于消掉 SQL 与判空里的裸字面量。
//
// 刻意不把这些列定义成具名类型（`type ConversationStatus string`）：会话/轮次状态的
// 权威定义在 `internal/manager/events.go`，那里是一组**无类型字符串常量**，
// 且 manager 全程按 string 传参（`markConversation(id, status string)`、
// `ConversationMetaPatch{Status: &status}`）。把列改成具名类型会连锁改动 manager 全包
// 与其测试，收益只是类型别名，故此处只统一取值、不改变类型身份。
const (
	// DecisionAllow 权限记忆的「允许」方向。
	DecisionAllow = "allow"
	// DecisionReject 权限记忆的「拒绝」方向。
	DecisionReject = "reject"

	// RoleUser 用户消息角色。
	RoleUser = "user"
	// RoleAssistant 助手消息角色。
	RoleAssistant = "assistant"

	// InputStatusQueued 队列项等待投递。
	InputStatusQueued = "queued"
	// InputStatusDelivered 队列项已投递（有 claimed_at）。
	InputStatusDelivered = "delivered"
	// InputStatusStale 队列项作废（进程重启 / 会话执行代际已变），保留行仅作审计。
	InputStatusStale = "stale"

	// InputDeliveryAfterTurn 排队，等当前轮结束再投递。
	InputDeliveryAfterTurn = "after_turn"
	// InputDeliveryInterruptThenPrompt 取消当前轮，随后投递（服务端原子化）。
	InputDeliveryInterruptThenPrompt = "interrupt_then_prompt"

	// MetaKeyEventsLastSeq 全局单调事件序号的计数行键（events.seq 的唯一分配源）。
	MetaKeyEventsLastSeq = "events_last_seq"
	// MetaKeyImportMarker transcript 导入标记：value = 导入目录指纹（幂等依据）。
	MetaKeyImportMarker = "imported_transcripts"
	// MetaKeyImportedAutomations automation 定义导入标记（与能力/agent 面同构）。
	MetaKeyImportedAutomations = "imported_automations"
	// MetaKeyImportedAgents agent 定义导入标记：value = <AgentDir> 内定义文件的指纹（幂等依据）。
	//
	// 与 MetaKeyImportedCapabilities 同构（见那里的注释）；分键而不是共用，是因为两个目录的
	// 指纹各自演进：只改能力目录不该让 agent 导入重跑一遍。
	MetaKeyImportedAgents = "imported_agents"
	// MetaKeyDefaultAgent 「当前 Agent」的显式选择（原 <AgentDir>/default.json 的 default_agent 值）。
	//
	// 它是**目录级配置**而不是某个 agent 的属性（`default_agent.go` 的语义），所以进 meta KV
	// 而不是 agents 表（计划 §7 P-8）；meta 表在 M6-6 之前只被会话侧使用，键名一律显式登记。
	MetaKeyDefaultAgent = "default_agent"
	// MetaKeyImportedCapabilities 能力定义导入标记：value = 导入目录（skills+mcp 文件清单）的指纹。
	//
	// 只作**装配期自动导入**的幂等依据（目录没变就整体跳过）；显式 `mate capability import`
	// 不看它——那条路径每次按行判重（新丢进目录一个文件，跑一次就该进来）。
	MetaKeyImportedCapabilities = "imported_capabilities"
	// MetaKeyImportedProviders provider 档案导入标记：value = `providers.json` 的内容指纹。
	//
	// 与 MetaKeyImportedCapabilities/MetaKeyImportedAgents 同构：装配期**一次性幂等**导入
	// （文件时代 → providers 表）的判据，只跑一次。文件**不删也不回写**（设计「数据迁移与校验」）。
	MetaKeyImportedProviders = "imported_providers"
	// MetaKeyProvidersImportMismatch provider 导入**对账不一致**的事实（value = 逐行描述）。
	//
	// 对账不一致**不阻断启动**（档案数量是几十条，不一致应当被人看见，而不是被静默放过或
	// 直接拒绝启动），所以它必须落成一个可被 `mate doctor` 读到的持久事实；一致时写空串清除。
	MetaKeyProvidersImportMismatch = "providers_import_mismatch"
	// MetaKeyRuntimeConfigDigest 运行时配置文件的物化摘要（T5）。
	//
	// value 是 JSON：`{"<runtime>": {"digest":..., "generated_at":..., "generator_version":...}}`。
	// 用途有三（设计「meta 新增键」）：① 判断是否需要重新物化；② 检测配置文件被外部改过
	// （摘要对不上 → 报冲突，不静默覆盖）；③ 回退时知道该删什么。
	MetaKeyRuntimeConfigDigest = "runtime_config_digest"
	// MetaKeyDefaultModel 「默认模型」的用户偏好（v12，ADR-21）：value = **一个**平台模型 id
	// （`models.id`），**不带 runtime 维度**。
	//
	// 为什么是单一值而不是逐 runtime 一张映射：默认模型是**用户的偏好**（"默认用这个"），
	// 而逐 runtime 存会把"是哪个底座的默认"逼回界面——v11 正是如此（面板上有"已设为该底座的
	// 默认模型"）。选 meta KV 而不是新表：它与既有的 `MetaKeyDefaultAgent` 完全同形，有先例。
	//
	// **交付时**由编排层把它派生成每个 runtime 各自的默认（`Set.DefaultModel`）——存储层不带
	// 底座维度，投影层才带。
	MetaKeyDefaultModel = "default_model"
	// MetaKeyMigrationNotes v12 迁移留下的**需人工处置的事实**（value = JSON）。
	//
	// 迁移不做"静默改变语义"的事，但有些东西 v12 确实没有出口（`extra`/`compat`/`package`
	// 等底座私有键、多 runtime 默认不一致、推导不出 `base_url`、原协议不是 openai）。
	// 这些**不阻断迁移**（B1：缺端点是一次性人工成本），但必须落成一个可被 `mate doctor`
	// 读到的持久事实——否则"未迁移字段"就成了一句没人能验证的话。
	MetaKeyMigrationNotes = "migration_notes"

	// CapabilityKindSkill 能力种类：skill（capability_revisions.kind）。
	CapabilityKindSkill = "skill"
	// CapabilityKindMcp 能力种类：mcp server（capability_revisions.kind）。
	CapabilityKindMcp = "mcp"
	// CapabilityKindAgent 定义种类：agent（M6-6；capability_revisions.kind）。
	CapabilityKindAgent = "agent"
	// CapabilityKindAutomation 定义种类：automation（M6-7；capability_revisions.kind）。
	CapabilityKindAutomation = "automation"
	// CapabilityKindProvider 定义种类：provider 档案（设置 · 模型；capability_revisions.kind）。
	CapabilityKindProvider = "provider"
	// CapabilityKindModel 定义种类：平台模型清单条目（设置 · 模型；capability_revisions.kind）。
	CapabilityKindModel = "model"

	// CapabilityActionCreate 新建定义。
	CapabilityActionCreate = "create"
	// CapabilityActionUpdate 覆盖定义。
	CapabilityActionUpdate = "update"
	// CapabilityActionDelete 删除定义。
	CapabilityActionDelete = "delete"
	// CapabilityActionImport 由存量文件一次性导入（M6-4）。
	CapabilityActionImport = "import"
	// CapabilityActorImport 导入动作的执行者（写面是 "rest"，导入是运维动作，两者在审计里可分辨）。
	CapabilityActorImport = "import"
)
