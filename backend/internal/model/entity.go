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
	TableCapabilityRevision = "capability_revisions"
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
	// WorkspaceTemporary 该工作空间是否由平台代建（未指定 cwd 时在 workspace_dir 下建）。
	// 权威标志：任何「临时空间」判定都必须读它，不得从路径形状反推。
	WorkspaceTemporary bool `gorm:"column:workspace_temporary;serializer:arkforge_flag"`
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
	Pinned            bool   `gorm:"column:pinned;serializer:arkforge_flag"`
	Archived          bool   `gorm:"column:archived;serializer:arkforge_flag"`
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
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	CreatedAt   time.Time `gorm:"column:created_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	StartedAt    time.Time `gorm:"column:started_at;not null;serializer:arkforge_rfc3339"`
	// EndedAt 终态时间；未结束 → 零值（落库为空串）。
	EndedAt time.Time `gorm:"column:ended_at;serializer:arkforge_rfc3339"`
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
	CreatedAt       time.Time `gorm:"column:created_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// ClaimedAt 被 claim（投递）的时刻；未投递 → 零值（落库为空串）。
	ClaimedAt time.Time `gorm:"column:claimed_at;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (Input) TableName() string { return TableInput }

// InputList 是 Input 切片类型（gormdao 泛型参数要求具名切片）。
type InputList []Input

// Migration 是 schema_migrations 表的一行（迁移内核自管的版本记录）。
type Migration struct {
	Version   int       `gorm:"column:version;primaryKey"`
	Name      string    `gorm:"column:name;not null"`
	AppliedAt time.Time `gorm:"column:applied_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
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
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (McpServerDefinition) TableName() string { return TableMcpServer }

// McpServerDefinitionList 是 McpServerDefinition 切片类型（gormdao 泛型参数要求具名切片）。
type McpServerDefinitionList []McpServerDefinition

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
	// Kind 能力种类。取值见 CapabilityKindSkill / CapabilityKindMcp。
	Kind string `gorm:"column:kind;not null"`
	// CapabilityID 被改动的能力 id（能力删除后本行仍留着它）。
	CapabilityID string `gorm:"column:capability_id;not null"`
	// Action 变更动作。取值见 CapabilityActionCreate / …Update / …Delete / …Import。
	Action string `gorm:"column:action;not null"`
	// Actor 变更发起者（当前是调用方标识，如 "rest" / "cli" / "import"）。
	Actor string `gorm:"column:actor;not null"`
	// At 变更时刻（RFC3339Nano(UTC)）。
	At time.Time `gorm:"column:at;not null;serializer:arkforge_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// Digest 提交后 body 的摘要（sha256 hex）：审计要能回答"那一版到底是什么"，
	// 而不是只留一个自增号。
	Digest string `gorm:"column:digest;not null"`
}

// TableName 实现 gormdao.Entity。
func (CapabilityRevision) TableName() string { return TableCapabilityRevision }

// CapabilityRevisionList 是 CapabilityRevision 切片类型（gormdao 泛型参数要求具名切片）。
type CapabilityRevisionList []CapabilityRevision
