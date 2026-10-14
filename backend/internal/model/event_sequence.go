package model

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameEventSequence 每会话事件号序计数器（v21；照 opencode 的 `event_sequence` 形状）。
	TableNameEventSequence = "event_sequence"
)

// EventSequenceEntity 是 `event_sequence` 表的一行：**一个会话一行**，
// `Seq` = 该会话**已分配到的**位次（刚建会话时是 0，于是第一条事件的 `seq` 也是 0）。
//
// 为什么独立成表（而不是继续用 `meta.events_last_seq` 这个全局计数行）：v21 起位次是
// **会话内**量（0 起、稠密），而"某会话下一个可用号"必须能被单独、原子地读写（单写者事务内
// upsert 取号）。全局计数行既表达不了"每会话一行"这件事，也会把"删一个会话"变成"顺手改另一张表"。
//
// 主键是 `SessionID`（列名 `session_id`）而不是 `id`：本表**没有**自己的身份——它就是那个会话的
// 计数器。这与 `schema_migrations.version` 同形；gormdao 的泛型主键假设只在 `GetByID` 一类方法里
// 才成立，本表走自定义的 upsert（见 `dao/event_sequence.go`）。
type EventSequenceEntity struct {
	// SessionID 会话 id（`ses_<26 位 ULID>`；主键）。
	SessionID string `gorm:"column:session_id;primaryKey"`
	// Seq 该会话已分配到的位次（0 起；`NextSeq` 返回 `seq+1` 并写回本列）。
	Seq int64 `gorm:"column:seq;not null"`
}

// TableName 实现 gormdao.Entity。
func (EventSequenceEntity) TableName() string { return TableNameEventSequence }

// EventSequenceEntityList 是 EventSequenceEntity 切片类型（gormdao 泛型参数要求具名切片）。
type EventSequenceEntityList []EventSequenceEntity
