package model

// 本文件是**持久层内部**使用的具名常量，用于消掉 SQL 与判空里的裸字面量。
//
// 刻意不把这些列定义成具名类型（`type SessionStatus string`）：会话/轮次状态的
// 权威定义在 `internal/domain/session/events.go`，那里是一组**无类型字符串常量**，
// 且 manager 全程按 string 传参（`markSession(id, status string)`、
// `SessionMetaPatch{Status: &status}`）。把列改成具名类型会连锁改动 manager 全包
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

	// InputDeliveryAfterRun 排队，等当前轮结束再投递。
	InputDeliveryAfterRun = "after_run"
	// InputDeliveryInterruptThenPrompt 取消当前轮，随后投递（服务端原子化）。
	InputDeliveryInterruptThenPrompt = "interrupt_then_prompt"

	// MetaKeyImportMarker transcript 导入标记：value = 导入目录指纹（幂等依据）。
	MetaKeyImportMarker = "imported_transcripts"
	// MetaKeyReadModelsRebuild **读模型需要重建**的标记（v21）：value = 让读模型失效的那一版号
	// （如 `"21"`）。空/不存在 = 读模型与 events 一致。
	//
	// 为什么用"标记 + 启动重建"而不是在迁移里逐列搬迁：`runs`/`messages` 是 events 的**投影**
	// （判据 J7），口径变化（位次值域、白名单、ordinal）的正确处置是**重建**而不是"把旧口径的
	// 数搬成新口径"——后者每个版本都要写一段一次性搬迁代码，而重建只有一个实现。
	//
	// 消费面是**唯一的**：`manager.RebuildSessionReadModels`（启动钩子 + `mate doctor
	// --rebuild-read-models`）。写完即清（清标记与重建同事务边界内应尽力而为）。
	MetaKeyReadModelsRebuild = "read_models_rebuild"
	// MetaKeySessionsModelRefBackfill **会话模型引用待回填**的标记（v26）：value = 置标记的那一版号
	// （`"26"`）。空/不存在 = 已回填完成（或不需要回填）。
	//
	// 为什么需要一个标记，而不是"在迁移里就地改写"：v26 起 `sessions.model_id` 装的是**平台模型行
	// id**（`mdl_…`），而存量装的是**底座的不透明值**（dsh 下是 `["deepseek-main","gpt-4o"]`）。
	// 两者之间的换算是"平台行 → 运行时值"，要过 adapter——而 `internal/persistence/store` **不得 import
	// adapter**（守卫 `TestInternalDoesNotImportRuntimeAdapters`），所以迁移只能置标记，真正的回填
	// 由持注册表的 `manager.backfillSessionModelRefs` 在启动时做。
	//
	// 消费面是**唯一的**（同 MetaKeyReadModelsRebuild 的纪律）：`backfillSessionModelRefs`。
	// **算不出来就不清标记**——"这次算不出来"不等于"永远算不出来"，打勾会把一次失败固化成事实。
	MetaKeySessionsModelRefBackfill = "sessions_model_ref_backfill"
	// MetaKeyImportedAutomations automation 定义导入标记（与能力面同构）。
	MetaKeyImportedAutomations = "imported_automations"
	// MetaKeyImportedCapabilities 能力定义导入标记：value = 导入目录（skills+mcp 文件清单）的指纹。
	//
	// 只作**装配期自动导入**的幂等依据（目录没变就整体跳过）；显式 `mate capability import`
	// 不看它——那条路径每次按行判重（新丢进目录一个文件，跑一次就该进来）。
	MetaKeyImportedCapabilities = "imported_capabilities"
	// MetaKeyImportedProviders provider 档案导入标记：value = `providers.json` 的内容指纹。
	//
	// 与 MetaKeyImportedCapabilities 同构：装配期**一次性幂等**导入
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
	// 默认模型"）。选 meta KV 而不是新表：它与既有的目录级偏好键（如 `default_model`）完全同形，有先例。
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
	// MetaKeyMigrationNotesV13 v13 迁移（providers/models 列化）留下的**需人工处置的事实**（value = JSON）。
	//
	// **刻意不复用 MetaKeyMigrationNotes**：v13 覆盖它等于把 v12 那些尚未处置的事实
	// （无出口字段 / 缺端点推导 / 默认模型冲突）静默抹掉——「同一处债不再借第二次」。
	MetaKeyMigrationNotesV13 = "migration_notes_v13"
	// MetaKeyMigrationNotesV14 v14 迁移（工作空间登记表）留下的**需人工处置的事实**（value = JSON）。
	//
	// 与 V13 同一条理由：复用 MetaKeyMigrationNotes/V13 会把前面那些尚未处置的事实静默抹掉
	// （「同一处债不再借第二次」）。v14 这里记的是"多少既有会话落在默认工作空间、代建临时
	// 目录还在磁盘上"——它是可读事实，不是待修的错误。
	MetaKeyMigrationNotesV14 = "migration_notes_v14"

	// DefinitionKindSkill 定义种类：skill（definition_revisions.kind）。
	//
	// 这一族**记的是「用户可编辑定义」的种别**（v16 之前叫 CapabilityKind*）：它的范围与
	// "能力面"（skill+mcp，见 `internal/core/capability`）和"底座能力声明面"（`CapabilityScope`）都不同，
	// 后两者各自占用了 capability 这个词——别再把本族叫回去（`TestDefinitionVocabulary`）。
	DefinitionKindSkill = "skill"
	// DefinitionKindMcp 定义种类：mcp server（definition_revisions.kind）。
	DefinitionKindMcp = "mcp"
	// DefinitionKindExpert 定义种类：expert（专家定义；v31；definition_revisions.kind）。
	DefinitionKindExpert = "expert"
	// DefinitionKindAutomation 定义种类：automation（M6-7；definition_revisions.kind）。
	DefinitionKindAutomation = "automation"
	// DefinitionKindProvider 定义种类：provider 档案（设置 · 模型；definition_revisions.kind）。
	DefinitionKindProvider = "provider"
	// DefinitionKindModel 定义种类：平台模型清单条目（设置 · 模型；definition_revisions.kind）。
	DefinitionKindModel = "model"

	// DefinitionActionCreate 新建定义。
	DefinitionActionCreate = "create"
	// DefinitionActionUpdate 覆盖定义。
	DefinitionActionUpdate = "update"
	// DefinitionActionDelete 删除定义。
	DefinitionActionDelete = "delete"
	// DefinitionActionImport 由存量文件一次性导入（M6-4）。
	DefinitionActionImport = "import"
	// DefinitionActorImport 导入动作的执行者（写面是 "rest"，导入是运维动作，两者在审计里可分辨）。
	DefinitionActorImport = "import"
)
