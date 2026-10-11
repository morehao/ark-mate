package model

import "time"

// 表名常量：DAO 与本包实体的唯一对照点（新增表在此登记）。
const (
	// TableMeta 计数 / 导入标记 / 任意键值。
	TableMeta = "meta"
	// TableConversation 会话权威元数据。
	TableConversation = "conversations"
	// TableEvent 事件事实源（唯一写入口见 store.AppendEvent）。
	TableEvent = "events"
	// TableRun 每轮 prompt 的物化行。
	TableRun = "runs"
	// TableMessage 消息物化投影（可从 events 重建）。
	TableMessage = "messages"
	// TableMemory 会话级权限记忆。
	TableMemory = "permission_memories"
	// TableMigration schema 版本记录（迁移内核自管）。
	TableMigration = "schema_migrations"
	// TableInput 待投递的用户输入队列（§6 busy input 队列）。
	TableInput = "inputs"
	// TableSkill 能力定义（skill）——sqlite 模式下它是能力面的**事实源**（M6-1）。
	TableSkill = "skills"
	// TableMcpServer MCP server 定义（与 TableSkill 同形，见 McpServerDefinition）。
	TableMcpServer = "mcp_servers"
	// TableCapabilityRevision 能力定义变更审计（**全局单调 seq = 代际 rev 的来源**）。
	//
	// 表名里的 capability 是历史命名：M6-6 起它同时承载 agent（以及 M6-7 的 automation）
	// 定义的变更——它就是"**定义变更**审计"这一件事，`kind` 列区分域。不改名是因为它已在
	// v8 落地（改名要动迁移内核与既有测试），而"能力面"在本仓的口径本来就含 Agent 视角。
	TableCapabilityRevision = "capability_revisions"
	// TableAgent agent 定义（M6-6；与 TableSkill 同形）。
	TableAgent = "agents"
	// TableAutomation 定时任务：**定义列 + 运行态列**（M6-7，Plan §7 P-10）。
	TableAutomation = "automations"
	// TableProvider provider 档案（供应商凭据）：一条上游凭据的登记（v11）。
	//
	// 它是 `providers.json` 的 DB 载体（设计 §「新表 providers」）：sqlite 模式下档案的事实源，
	// memory 模式下文件仍是事实源，写面按持久层模式分派（`providers_carrier.go`）。
	TableProvider = "providers"
	// TableModel 平台模型清单（v11）：一条模型条目 = (runtime, provider_id, model_id)。
	//
	// 它是**清单的唯一来源**，物化成各 runtime 的配置文件（T5 `config_write`）；
	// 运行时实际广告的目录是「事实」，不落这张表（设计 D-6 / ADR-10）。
	TableModel = "models"
	// TableWorkspace 工作空间登记表（v14）：一条记录 = 一个规范化目录 + 展示名 + 顺序。
	//
	// 它是「用户的工作目录」这条线的**持久身份**（设计 ADR-1）：在这之前目录名只从会话
	// 派生（`DISTINCT cwd`），没有可写对象、也就没有"创建工作空间"这件事可落。
	TableWorkspace = "workspaces"
)

// 关于 autoCreateTime/autoUpdateTime：gorm 按字段名约定会把 CreatedAt/UpdatedAt
// 自动当成「插入/更新时取当前时间」，在 Create/Updates 时隐式覆盖调用方传入的值。
// 本仓的时间全部由调用方显式赋值（含「零值落空串」语义），故一律显式关闭。
//
// Meta 是 meta 表的一行（任意键值；events_last_seq 与导入标记都落在这里）。
type Meta struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value;not null"`
}

// TableName 实现 gormdao.Entity。
func (Meta) TableName() string { return TableMeta }

// MetaList 是 Meta 切片类型（gormdao 泛型参数要求具名切片）。
type MetaList []Meta

// Conversation 是 conversations 表的一行（DB 权威元数据；字段对齐 manager.ConversationRec + transcript header）。
type Conversation struct {
	ID string `gorm:"column:id;primaryKey"`
	// AgentID 归属 agent。
	AgentID string `gorm:"column:agent_id;not null"`
	// Cwd 会话工作空间（canonical 绝对路径；= 底座 ACP session 的 cwd，
	// 也是原生权限 workspace-write 的边界）。
	Cwd string `gorm:"column:cwd;not null"`
	// WorkspaceID 归属的工作空间登记行（v14；'' = 未登记/登记已删 → 读面归到**默认工作空间**）。
	//
	// 与 Cwd 的分工是本表最容易踩的一处，刻意记在这里：
	//   - **Cwd 是执行事实**：底座 ACP session 拿它当 cwd、原生权限按它画沙箱边界，
	//     它是历史事实、不可变，重建/续聊要按它重放。它**不能**由 WorkspaceID 推导——
	//     工作空间被删登记、或目录后来不可用时，历史会话的 cwd 必须仍然读得出来；
	//   - **WorkspaceID 是归属事实**：只用于「这条会话属于哪个分组」。它与 Cwd 在大多数行上
	//     指向同一目录，但两者不一致是**允许的**（设计 ADR-2/ADR-5），不是脏数据。
	//
	// 刻意**不加外键**：本仓 SQLite driver 不注入 `foreign_keys`（AGENTS.md 已登记该代价），
	// 写了也不生效；且删登记**本来就不该**连带删会话（设计 ADR-6），级联语义恰恰是错的。
	WorkspaceID string `gorm:"column:workspace_id;not null;default:''"`
	// WorkspaceTemporary 该工作空间是否由平台代建（未指定 cwd 时在 workspace_dir 下建）。
	// 权威标志：任何「临时空间」判定都必须读它，不得从路径形状反推。
	WorkspaceTemporary bool `gorm:"column:workspace_temporary;serializer:arkmate_flag"`
	// Provider provider 档案 id（会话级模型选择；空 = 跟随 agent）。
	Provider string `gorm:"column:provider;not null"`
	// Model 会话级模型 id（空 = 底座默认）。
	Model string `gorm:"column:model;not null"`
	// RuntimeHint 最近/首选 runtime（执行提示，非身份，用户不可见）。
	RuntimeHint string `gorm:"column:runtime_hint;not null"`
	// ProviderSessionID 原生底座会话 id（opencode ses_*/dsh uuid）。P3 双 id：
	// conversations.id = 平台 uuid（对外稳定、隐藏底座），本列为恢复承载（load/resume）。
	ProviderSessionID string `gorm:"column:provider_session_id;not null"`
	Title             string `gorm:"column:title;not null"`
	Pinned            bool   `gorm:"column:pinned;serializer:arkmate_flag"`
	Archived          bool   `gorm:"column:archived;serializer:arkmate_flag"`
	// Status 会话状态。取值权威定义在 internal/manager/events.go（ConversationStatus*）。
	Status string `gorm:"column:status;not null"`
	// Origin 会话来源（M3；'' = 人工创建，'automation:<id>' = 该 automation 触发产生）。
	// 单独成列而不是塞进 capability_json：会话列表要逐行渲染"这是不是自动跑出来的"，
	// 而逐行解析 JSON 是 O(n) 的解析成本、也让 SQL 侧的过滤/索引无从下手。
	Origin string `gorm:"column:origin;not null"`
	// CapabilityJSON 创建时解析出的**能力选定明细**（JSON；'' = 未声明 → 回落 agent 白名单）。
	CapabilityJSON string `gorm:"column:capability_json;not null"`
	// CapabilityFingerprint 该会话**钉住的**能力指纹（代际）。单独成列是为了代际回收能按它
	// 走索引查"还有哪些会话在用这个代际"；塞进 JSON 就得全表扫 + 逐行解析。
	// '' = 未声明能力选择（与未引入代际时的行为逐字相同）。
	CapabilityFingerprint string `gorm:"column:capability_fingerprint;not null"`
	// LastSeq 该会话已落库事件的最后 seq（events.seq 全局单调）。
	LastSeq   int64     `gorm:"column:last_seq"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Conversation) TableName() string { return TableConversation }

// ConversationList 是 Conversation 切片类型（gormdao 泛型参数要求具名切片）。
type ConversationList []Conversation

// ConversationMetaPatch 会话元数据子集更新（nil = 不更新；updated_at 恒刷新）。
type ConversationMetaPatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
	Status   *string
	Provider *string
	Model    *string
}

// Empty 判断补丁是否为空（空补丁不产生 SQL）。
func (p ConversationMetaPatch) Empty() bool {
	return p.Title == nil && p.Pinned == nil && p.Archived == nil &&
		p.Status == nil && p.Provider == nil && p.Model == nil
}

// Event 是 events 表的一行（facts 的唯一事实源）。
type Event struct {
	// Seq 全局单调序号（meta.events_last_seq 是唯一分配源，见 store 的分配事务）。
	Seq            int64  `gorm:"column:seq;primaryKey"`
	ConversationID string `gorm:"column:conversation_id;not null"`
	// Type 事件类型。取值权威定义在 internal/manager/events.go。
	Type string `gorm:"column:type;not null"`
	// PayloadJSON 事件载荷（一律序列化为 JSON 文本，含结构体载荷）。
	PayloadJSON string    `gorm:"column:payload_json;not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Event) TableName() string { return TableEvent }

// EventList 是 Event 切片类型（gormdao 泛型参数要求具名切片）。
type EventList []Event

// Run 是 runs 表的一行（一轮 prompt 的物化行，turn 收口处写入）。
type Run struct {
	ID             string `gorm:"column:id;primaryKey"`
	ConversationID string `gorm:"column:conversation_id;not null"`
	UserText       string `gorm:"column:user_text;not null"`
	// Status 轮次终态。取值权威定义在 internal/manager/events.go。
	Status     string `gorm:"column:status;not null"`
	ErrorCode  string `gorm:"column:error_code;not null"`
	StopReason string `gorm:"column:stop_reason;not null"`
	UsageJSON  string `gorm:"column:usage_json;not null"`
	FirstSeq   int64  `gorm:"column:first_seq"`
	LastSeq    int64  `gorm:"column:last_seq"`
	// FirstTokenMS 本轮从用户消息到首个 assistant 输出（文本/思考/工具）的耗时（ms）；0 = 未记录。
	FirstTokenMS int64     `gorm:"column:first_token_ms"`
	StartedAt    time.Time `gorm:"column:started_at;not null;serializer:arkmate_rfc3339"`
	// EndedAt 终态时间；未结束 → 零值（落库为空串）。
	EndedAt time.Time `gorm:"column:ended_at;serializer:arkmate_rfc3339"`
}

// TableName 实现 gormdao.Entity。
func (Run) TableName() string { return TableRun }

// RunList 是 Run 切片类型（gormdao 泛型参数要求具名切片）。
type RunList []Run

// Message 是 messages 表的一行（消息物化：role/text + 交错时间线 + 工具最终态）。
type Message struct {
	ID             int64  `gorm:"column:id;primaryKey;autoIncrement"`
	ConversationID string `gorm:"column:conversation_id;not null"`
	RunID          string `gorm:"column:run_id;not null"`
	// Role 消息角色。取值见 RoleUser / RoleAssistant。
	Role            string    `gorm:"column:role;not null"`
	Text            string    `gorm:"column:text;not null"`
	EventsJSON      string    `gorm:"column:events_json;not null"`
	ToolDetailsJSON string    `gorm:"column:tool_details_json;not null"`
	SeqLo           int64     `gorm:"column:seq_lo"`
	SeqHi           int64     `gorm:"column:seq_hi"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Message) TableName() string { return TableMessage }

// MessageList 是 Message 切片类型（gormdao 泛型参数要求具名切片）。
type MessageList []Message

// Memory 是 permission_memories 表的一行（会话级权限记忆，allow/reject 双侧对称）。
type Memory struct {
	ID             int64  `gorm:"column:id;primaryKey;autoIncrement"`
	ConversationID string `gorm:"column:conversation_id;not null"`
	Title          string `gorm:"column:title;not null"`
	Kind           string `gorm:"column:kind;not null"`
	// Decision 决策方向。取值见 DecisionAllow / DecisionReject。
	Decision  string    `gorm:"column:decision;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Memory) TableName() string { return TableMemory }

// MemoryList 是 Memory 切片类型（gormdao 泛型参数要求具名切片）。
type MemoryList []Memory

// Input 是一条「排队待投递」的用户输入（§6 busy input 队列）。
//
// 为什么落库：会话正忙时用户仍能提交（after_turn / interrupt_then_prompt），这条输入在
// 当前轮结束前必须**活过进程重启之外的一切**——内存队列会让「提交成功但进程重启」变成静默丢失。
// 台账行同时是审计：谁在什么时候排了什么、最终投递了还是被撤下/作废。
type Input struct {
	ID             string `gorm:"column:id;primaryKey"`
	ConversationID string `gorm:"column:conversation_id;not null"`
	// Text 用户原文（投递时原样作为 prompt 发出）。
	Text string `gorm:"column:text;not null"`
	// Delivery 投递模式。取值见 InputDeliveryAfterTurn / InputDeliveryInterruptThenPrompt
	// （入队路径不会写 reject：reject 是「不排队，直接报错」）。
	Delivery string `gorm:"column:delivery;not null"`
	// TargetTurnID 入队时正在执行的那一轮（审计：用户排在哪一轮后面；不作投递判据——
	// 那一轮必然先结束，否则队列永远不会被消费）。
	TargetTurnID string `gorm:"column:target_turn_id;not null"`
	// Generation 入队时会话执行代际。投递前比对：不等即作废（旧队列项不得投进
	// 已重建过底座会话的上下文，见 manager 的 ConversationRec.Generation）。
	Generation int64 `gorm:"column:generation;not null"`
	// Status 队列项状态。取值见 InputStatusQueued / InputStatusDelivered / InputStatusStale。
	Status    string    `gorm:"column:status;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// ClaimedAt 被 claim（投递）的时刻；未投递 → 零值（落库为空串）。
	ClaimedAt time.Time `gorm:"column:claimed_at;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Input) TableName() string { return TableInput }

// InputList 是 Input 切片类型（gormdao 泛型参数要求具名切片）。
type InputList []Input

// Migration 是 schema_migrations 表的一行（迁移内核自管的版本记录）。
type Migration struct {
	Version   int       `gorm:"column:version;primaryKey"`
	Name      string    `gorm:"column:name;not null"`
	AppliedAt time.Time `gorm:"column:applied_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Migration) TableName() string { return TableMigration }

// MigrationList 是 Migration 切片类型（gormdao 泛型参数要求具名切片）。
type MigrationList []Migration

// ——— 能力定义事实源（M6-1；表名见 TableSkill / TableMcpServer） ———

// SkillDefinition 是一条 skill 定义行。
//
// `Body` 是**唯一权威**：定义原始 JSON 原样保存，未知键因此得以承载——落盘纪律（C9）的
// **意图保留、机制换掉**：从"读 map → 只改目标键 → 原子替换文件"换成"事务 + 行 revision CAS"
// （计划 §1 P-2/P-3）。
//
// `Revision`/`UpdatedAt` 是仅有的两个**提升列**。刻意**不加** `enabled`/`scope`/`runtimes`
// 等派生列：它们都在 `body` 里，双写必然漂移；能力定义规模是几十条，查询在 Go 侧做即可。
type SkillDefinition struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义原始 JSON（含未知键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据：从 1 起，每次成功提交 +1。
	//
	// 它就是今天 `checkUnchanged`（mtime+size）所处的位置——对外表现仍是同一个
	// `definition_changed`(40901)，**名字与码都不变**。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (SkillDefinition) TableName() string { return TableSkill }

// SkillDefinitionList 是 SkillDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type SkillDefinitionList []SkillDefinition

// McpServerDefinition 是一条 MCP server 定义行（列与语义同 SkillDefinition）。
//
// 与 SkillDefinition 分表而不是加 kind 列：两张表的读取面、写面与规模都各走各的，
// 合成一张"定义表 + kind"会让每个查询都带一个恒真的过滤条件，且 kind 与 id 的
// 唯一性约束会变成"跨 kind 唯一"这种没人想要的东西。
type McpServerDefinition struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义原始 JSON（含未知键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillDefinition.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (McpServerDefinition) TableName() string { return TableMcpServer }

// McpServerDefinitionList 是 McpServerDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type McpServerDefinitionList []McpServerDefinition

// AgentDefinition 是一条 agent 定义行（M6-6；列与语义同 SkillDefinition）。
//
// **只存用户对种子的覆盖**：内置种子（`builtinAgents`：dsh/opencode）永不落行，装载时先铺种子
// 再用行里的**非零字段**覆盖（`applyDefinition` 的部分合并语义逐字保留）。所以"DB 里有几行"
// ≠ "平台有几个 agent"——`List` 出来的行里可能只有 `{"id":"x","name":"..."}`。
//
// Body 存的是 **typed marshal 的结果**（与今天 `persistAgentDef` 写文件的内容逐字节同源），
// 不是"原始文件字节"：agent 的写面今天就走 typed 结构（`agentDefinitionFile`），DB 化只换载体
// 不换形状——未知键在 agent 侧从来就没被保留过，这一条不在 M6-6 的语义变更范围里。
type AgentDefinition struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义 JSON（typed marshal；见类型注释）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillDefinition.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (AgentDefinition) TableName() string { return TableAgent }

// AgentDefinitionList 是 AgentDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type AgentDefinitionList []AgentDefinition

// AutomationDefinition 是一条定时任务（M6-7）。
//
// **定义与运行态分列**（Plan §7 P-10）：`Body` 只放用户写的定义（含 `state`——它刻意不在
// `automation.ReservedKeys` 里，见那里的注释：启用/停用/归档本来就是用户操作），运行态
// （`last_fired_at` / `last_status` / … / `recent_runs`）走列。于是"用户往 body 里塞运行态键"
// 这件事**结构上不可能**，而拒写名单（`IsReservedKey` → 40001）仍然保留——否则用户会写出一个
// "我写了它为什么不生效"的定义。
//
// **运行态列不参与 revision CAS**：`Revision`/`UpdatedAt` 只记定义（含 `state`，它是定义键）
// 的提交。调度器每次推进只改运行态列 → 面板上的定义不会看起来"被别人改过"（文件载体里这一步
// 会抬高 mtime；DB 载体把两件事分开之后，只有定义被真改过才撞 40901）。唯一例外是**首次**
// 推进要往正文里补一个 `state`（用户没写过它），那一次会前进一版——之后逐字节稳定。
//
// `recent_runs` 存 JSON 文本（有界，`automation.RecentRunsMax`）：搬进 `events` 表会撞
// `events.conversation_id NOT NULL REFERENCES conversations`，而 duplicate/overlap/skipped
// 三类结果**没有会话可挂**（Plan §7 P-11）。
type AutomationDefinition struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 用户写的定义（原始 JSON，含未知键；**不含**运行态键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillDefinition.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次**定义**提交的时刻（RFC3339Nano(UTC)；运行态写回不刷新它）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// LastFiredAt 最近一次被认领的 occurrence（RFC3339Nano(UTC)；空 = 从未触发）。
	//
	// 它是"这个 occurrence 是否已经触发过"的**唯一判据**（ADR-10），也是调度推进的 CAS 判据：
	// 认领 = `WHERE id=? AND last_fired_at=<读到的值>`，0 行 = 别人先认领了。
	LastFiredAt string `gorm:"column:last_fired_at;not null;default:''"`
	// LastStatus 最近一次结果（completed/failed/interrupted/skipped）。
	LastStatus string `gorm:"column:last_status;not null;default:''"`
	// LastReason 最近一次没跑成/失败的原因（人话）。
	LastReason string `gorm:"column:last_reason;not null;default:''"`
	// LastConversationID 最近一次触发产生的会话（overlap 判据用它查会话是否还在跑）。
	LastConversationID string `gorm:"column:last_conversation_id;not null;default:''"`
	// ConsecutiveFailures 连续失败次数（跑成一次即清零）。
	ConsecutiveFailures int64 `gorm:"column:consecutive_failures;not null;default:0"`
	// RecentRuns 最近若干次触发的留痕（JSON 数组文本，最新在前，有界）。
	RecentRuns string `gorm:"column:recent_runs;not null;default:'[]'"`
}

// TableName 实现 gormdao.Entity。
func (AutomationDefinition) TableName() string { return TableAutomation }

// AutomationDefinitionList 是 AutomationDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type AutomationDefinitionList []AutomationDefinition

// ProviderDefinition 是一条 provider 档案行（**资源表**形态，v13 列化）。
//
// 它**不再是定义载体**：`skills` / `mcp_servers` / `agents` / `automations` 的 body 是
// 「用户自由正文」，而 provider 的键集合 `{name, protocol, base_url, note}` 是平台拥有的
// **封闭字段集**且每个键都在热路径上（`protocol` 决定适配、`base_url` 决定请求落点）——
// 封闭 ⇒ 列化（ADR-1/ADR-2）。判据一句话：body 只留给「正文是用户自由文档」的资源。
//
// **刻意没有的两列**（每条都是判据，不是遗漏）：
//   - `body`：键集合封闭，它的缺席由 archtest 守卫钉住；
//   - `key_ref`：它是 `providerRef(id)` 的纯函数副本，且 vault 的 AAD 已绑定
//     `vault:providers/<id>`；两份值不一致时的表现是「凭据坏了」，会误导排查（ADR-7）。
//     `has_key` 一律由 `vault.Has(providerRef(id))` 现算。
//
// **明文密钥结构上进不来**：没有任何一列能容纳它（ADR-8/ADR-15）。
type ProviderDefinition struct {
	// ID 主键（词法受限：它会进 vault 引用段）。
	ID string `gorm:"column:id;primaryKey"`
	// Name 展示名（空 = 前端回落 id）。
	Name string `gorm:"column:name;not null;default:''"`
	// Protocol 协议（本期只有 openai；库级 CHECK 兜住裸 SQL 路径）。
	Protocol string `gorm:"column:protocol;not null"`
	// BaseURL 上游端点（空 = 缺端点，界面与 doctor 显式标出，且该供应商不进任何 Set）。
	BaseURL string `gorm:"column:base_url;not null;default:''"`
	// Note 备注（不驱动行为）。
	Note string `gorm:"column:note;not null;default:''"`
	// Revision 写面 CAS 判据（同 SkillDefinition.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// CreatedAt 创建时刻（RFC3339Nano(UTC)）。
	//
	// **存量行是近似值**：v11/v12 都没建这一列，v13 迁移只能取 `updated_at` 并登记
	// （`migration_notes_v13.created_at_approximated`）——如实登记，不假装它是创建时间。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ProviderDefinition) TableName() string { return TableProvider }

// ProviderDefinitionList 是 ProviderDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type ProviderDefinitionList []ProviderDefinition

// ModelDefinition 是平台声明的一条模型条目（v12 重建、v13 列化；**不带底座维度**）。
//
// 一条模型条目说清「挂在哪条供应商下、发给上游的是哪个模型 id」——它**不再**说"给哪个底座用"：
// 一份清单同时投给所有已注册底座（ADR-1、B3）。v11 的身份三元组 `(runtime, provider_id, model_id)`
// 因此塌成两列：`(provider_id, model_id)` **唯一**（库级唯一索引真表达得了，见 v12 迁移）。
//
// v13 把仅剩的两个平台字段（`name` / `note`）与新增的 `input_modalities` 提成列，
// `body` 随之消失——理由与 provider 相同：键集合封闭、每次列表渲染都要用（ADR-1）。
// 于是「一行坏 JSON 打不开整页」在结构上不可能（没有 JSON 可比坏，v12 的 P2 收尾）。
//
// 默认模型**不在这里**：它是**用户的偏好**、是单一值，住 `meta.MetaKeyDefaultModel`（ADR-21）。
type ModelDefinition struct {
	ID string `gorm:"column:id;primaryKey"`
	// ProviderID 挂靠的供应商 id（指向 providers.id）。引用完整性由 store 的**同一事务**保证
	// （不依赖 FK：本仓 SQLite 的 `foreign_keys=0`，所有 REFERENCES 都是装饰，ADR-4）。
	ProviderID string `gorm:"column:provider_id;not null"`
	// ModelID 发给上游的模型 id（**不是**平台模型 id，那是 ID 列）。
	ModelID string `gorm:"column:model_id;not null"`
	// Name 展示名（空 = 界面回落 ModelID）。
	Name string `gorm:"column:name;not null;default:''"`
	// Note 备注（不驱动行为）。
	Note string `gorm:"column:note;not null;default:''"`
	// InputModalities 输入模态（两态：`text` | `text,image`；**默认 `text`**）。
	//
	// 它是平台生成的 route 声明里 `models[].input` 的来源，由**逗号分隔**存成一列
	// （两态取值空间很小，第二张表/JSON 列都不划算）。**总是带值**：`text` 是明确值而不是
	// "没设"，所以 dsh adapter 总是写 `input`（ADR-12 的代价段：会显式覆盖底座 catalog）。
	InputModalities string `gorm:"column:input_modalities;not null;default:'text'"`
	// Revision 写面 CAS 判据（同 SkillDefinition.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// CreatedAt 创建时刻（RFC3339Nano(UTC)；存量行是 `updated_at` 近似值，见 ProviderDefinition.CreatedAt）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ModelDefinition) TableName() string { return TableModel }

// ModelDefinitionList 是 ModelDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type ModelDefinitionList []ModelDefinition

// CapabilityRevision 是一条能力定义变更审计。
//
// **刻意不加外键**：审计必须在能力被删除后仍然存在——这与 `events` 表"必须挂会话"
// （`events.conversation_id NOT NULL REFERENCES conversations(id)`）的约束正好相反，别照抄那套。
//
// `Seq` 是 `AUTOINCREMENT`（单调、不复用），因此可以直接当作设计文档 §3.3/§10.5-③ 的
// "当前代际 rev"：M1 落地时它取 `MAX(seq)` 即可，不需要另造一个进程内计数器
// （设计文档 §"M6 与 M1 有直接交叠"那条建议）。
//
// 注意本表**没有 `id` 列**（主键叫 `seq`）：gormdao 以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用，见 `internal/dao/doc.go`。
type CapabilityRevision struct {
	// Seq 全局单调序（AUTOINCREMENT 保证不复用）。
	Seq int64 `gorm:"column:seq;primaryKey;autoIncrement"`
	// Kind 定义种类。取值见 CapabilityKindSkill / CapabilityKindMcp / CapabilityKindAgent
	// （M6-7 起还有 CapabilityKindAutomation）。
	Kind string `gorm:"column:kind;not null"`
	// CapabilityID 被改动的定义 id（定义删除后本行仍留着它）。
	CapabilityID string `gorm:"column:capability_id;not null"`
	// Action 变更动作。取值见 CapabilityActionCreate / …Update / …Delete / …Import。
	Action string `gorm:"column:action;not null"`
	// Actor 变更发起者（当前是调用方标识，如 "rest" / "cli" / "import"）。
	Actor string `gorm:"column:actor;not null"`
	// At 变更时刻（RFC3339Nano(UTC)）。
	At time.Time `gorm:"column:at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// Digest 提交后 body 的摘要（sha256 hex）：审计要能回答"那一版到底是什么"，
	// 而不是只留一个自增号。
	Digest string `gorm:"column:digest;not null"`
}

// TableName 实现 gormdao.Entity。
func (CapabilityRevision) TableName() string { return TableCapabilityRevision }

// CapabilityRevisionList 是 CapabilityRevision 切片类型（gormdao 泛型参数要求具名切片）。
type CapabilityRevisionList []CapabilityRevision

// Workspace 是 workspaces 表的一行（v14）：**工作空间登记项**。
//
// 它是「用户在哪个目录里工作」这条线的持久实体（设计 §3.3 / ADR-1）。三个不变量：
//
//   - **一个规范化目录至多一条登记**：`path` 上的唯一索引是唯一执行者。为什么不在 Go 侧
//     判重：并发两次 POST 会各自读到"不存在"然后各插一行，库级唯一索引是唯一能挡住它的东西；
//   - **有且只有一个默认工作空间**：`is_default = 1` 的部分唯一索引保证。它是「建会话未指定
//     cwd」这条路的落点（设计 ADR-4），删掉它等于让那条路无路可走；
//   - **顺序是这一列的整数**：`list_order` 升序即左栏展示顺序。设计 ADR-3 说明了为什么不用
//     独立顺序表——规模是"用户手动登记的项目数"，区间 ±1 的代价可以忽略。
//
// **刻意没有的列**（每条都是判据，不是遗漏）：
//   - `session_ids` / 任何成员账：成员关系由 `conversations.workspace_id` 表达（设计 ADR-5）。
//     dsh 的登记行里有顺序化的 `sessionIds`，那是因为它的会话账独立于会话行、会漂移；
//     本仓归属就在会话行自己身上，结构上不可能漂移，再存一份账只会多一条会不一致的路径；
//   - 目录状态（是否存在/可读）：**不缓存**。外部进程随时可能删/移目录，缓存只会让界面
//     显示一个过期的好状态；读面每次现算。
type Workspace struct {
	// ID 平台 uuid（对外稳定标识）。**不是路径**：改名/搬迁不该让 id 漂移。
	ID string `gorm:"column:id;primaryKey"`
	// Path 规范化后的绝对路径（EvalSymlinks + Clean 之后）。
	// 唯一索引落在这一列上，"同一目录一个工作空间"由库保证。
	Path string `gorm:"column:path;not null"`
	// Title 展示名（'' = 未命名 → 前端按末级目录名回落渲染）。
	//
	// 服务端**不**在空标题时替前端编一个名字：那会把"未命名"这个状态永久写死成某个语言
	// 的字符串（与默认工作空间的语言中立口径冲突，见 IsDefault）。
	Title string `gorm:"column:title;not null;default:''"`
	// IsDefault 是否平台默认工作空间（有且只有一个；删不掉、身份不可改）。
	//
	// 它的标题在库里存的是语言中立的 `default`，**展示时**才按界面语言渲染成「默认工作空间」
	// ——与 dsh 的 workspaceDisplayTitle 同一手法：一次安装跨语言切换时磁盘路径与库里
	// 的标题都不变，只有屏幕上的字跟着变。
	IsDefault bool `gorm:"column:is_default;serializer:arkmate_flag"`
	// ListOrder 左栏展示顺序（升序；新登记的追加到尾部）。
	ListOrder int64 `gorm:"column:list_order;not null"`
	// CreatedAt 登记时刻（RFC3339Nano(UTC)）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次改写时刻（改名/重排都会刷新它）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Workspace) TableName() string { return TableWorkspace }

// WorkspaceList 是 Workspace 切片类型（gormdao 泛型参数要求具名切片）。
type WorkspaceList []Workspace
