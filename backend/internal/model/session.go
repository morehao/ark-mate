package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameSession 会话权威元数据。
	TableNameSession = "sessions"
)

// SessionEntity 是 sessions 表的一行（DB 权威元数据；字段对齐 manager.SessionRec + transcript header）。
type SessionEntity struct {
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
	// ProviderID provider 档案 id（会话级模型选择；空 = 跟随 agent）。
	//
	// v16 起列名带 `_id`（原 `provider`）：**库里这一列是引用**（指向 `providers.id`），
	// 于是它与 `models.provider_id` 同名同义；而 wire 上的 `provider` 是**选择器**
	// （"选哪个供应商"），所以 DTO/前端字段名不变（设计 D5：库里的列是引用 → `_id` 结尾，
	// 线上的字段是选择器 → 原名）。
	ProviderID string `gorm:"column:provider_id;not null"`
	// ModelID 会话级模型 id（空 = 底座默认）。指向 `models.model_id`，**不是** `models.id`——
	// 后者是平台铸造的 `mdl_<ULID>` 行身份，前者才是底座认的模型名。v16 前叫 `model`。
	ModelID string `gorm:"column:model_id;not null"`
	// RuntimeHint 最近/首选 runtime（执行提示，非身份，用户不可见）。
	RuntimeHint string `gorm:"column:runtime_hint;not null"`
	// ProviderSessionID 原生底座会话 id（opencode ses_*/dsh uuid）。P3 双 id：
	// sessions.id = 平台 uuid（对外稳定、隐藏底座），本列为恢复承载（load/resume）。
	ProviderSessionID string `gorm:"column:provider_session_id;not null"`
	Title             string `gorm:"column:title;not null"`
	Pinned            bool   `gorm:"column:pinned;serializer:arkmate_flag"`
	Archived          bool   `gorm:"column:archived;serializer:arkmate_flag"`
	// Status 会话状态。取值权威定义在 internal/manager/events.go（SessionStatus*）。
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
func (SessionEntity) TableName() string { return TableNameSession }

// SessionEntityList 是 SessionEntity 切片类型（gormdao 泛型参数要求具名切片）。
type SessionEntityList []SessionEntity

// SessionMetaPatch 会话元数据子集更新（nil = 不更新；updated_at 恒刷新）。
type SessionMetaPatch struct {
	Title      *string
	Pinned     *bool
	Archived   *bool
	Status     *string
	ProviderID *string
	ModelID    *string
}

// Empty 判断补丁是否为空（空补丁不产生 SQL）。
func (p SessionMetaPatch) Empty() bool {
	return p.Title == nil && p.Pinned == nil && p.Archived == nil &&
		p.Status == nil && p.ProviderID == nil && p.ModelID == nil
}
