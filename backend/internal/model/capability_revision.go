package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameCapabilityRevision 能力定义变更审计（**全局单调 seq = 代际 rev 的来源**）。
	//
	// 表名里的 capability 是历史命名：M6-6 起它同时承载 agent（以及 M6-7 的 automation）
	// 定义的变更——它就是"**定义变更**审计"这一件事，`kind` 列区分域。不改名是因为它已在
	// v8 落地（改名要动迁移内核与既有测试），而"能力面"在本仓的口径本来就含 Agent 视角。
	TableNameCapabilityRevision = "capability_revisions"
)

// CapabilityRevisionEntity 是一条能力定义变更审计。
//
// **刻意不加外键**：审计必须在能力被删除后仍然存在——这与 `events` 表"必须挂会话"
// （`events.session_id NOT NULL REFERENCES sessions(id)`）的约束正好相反，别照抄那套。
//
// `Seq` 是 `AUTOINCREMENT`（单调、不复用），因此可以直接当作设计文档 §3.3/§10.5-③ 的
// "当前代际 rev"：M1 落地时它取 `MAX(seq)` 即可，不需要另造一个进程内计数器
// （设计文档 §"M6 与 M1 有直接交叠"那条建议）。
//
// 注意本表**没有 `id` 列**（主键叫 `seq`）：gormdao 以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用，见 `internal/dao/doc.go`。
type CapabilityRevisionEntity struct {
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
func (CapabilityRevisionEntity) TableName() string { return TableNameCapabilityRevision }

// CapabilityRevisionEntityList 是 CapabilityRevisionEntity 切片类型（gormdao 泛型参数要求具名切片）。
type CapabilityRevisionEntityList []CapabilityRevisionEntity
