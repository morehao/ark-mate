package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameAutomationOutbox 自动化触发的**投递队列**（v33 / T5）。
	TableNameAutomationOutbox = "automation_outbox"
)

// AutomationOutboxEntity 一条待投递的自动化触发（v33）。
//
// 为什么需要它：自动化"跑完"与"结果送达"原先是一条路——`host.CreateSession` 一失败，那一轮就
// 只剩一句 `reason` 留在 `recent_runs` 里，**没有可重试的落点**（底座一时不可用 = 这个
// occurrence 永久丢掉）。有了这张表，执行侧只负责"把要投递的东西写下来"，投递由 worker 独立
// 推进、退避重试、按幂等键去重。
//
// **只存投递入参，不存运行结果**：结果在会话里（ADR-4「不回写会话结果」没有变）。
type AutomationOutboxEntity struct {
	// ID 自增主键（本表没有对外读面）。
	ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
	// AutomationID 触发它的自动化定义 id。
	AutomationID string `gorm:"column:automation_id;not null"`
	// Occurrence 本次 occurrence（RFC3339Nano(UTC) 文本；比较必须逐字节）。
	Occurrence string `gorm:"column:occurrence;not null;default:''"`
	// Trigger 触发来源（scheduled / recovery）。
	Trigger string `gorm:"column:trigger;not null"`
	// Payload 投递入参 JSON（`automation.HostSessionReq` 的直投影）。
	Payload string `gorm:"column:payload;not null"`
	// IdempotencyKey 幂等键（库级唯一）——"补投且只投一次"的第一道闸。
	IdempotencyKey string `gorm:"column:idempotency_key;not null"`
	// Status pending | sent | failed。
	Status string `gorm:"column:status;not null"`
	// Attempts 已尝试次数。
	Attempts int64 `gorm:"column:attempts;not null;default:0"`
	// NextAttemptAt 下一次允许尝试的时刻。
	NextAttemptAt time.Time `gorm:"column:next_attempt_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// LastError 最后一次失败原因（人话、已脱敏）。
	LastError string `gorm:"column:last_error;not null;default:''"`
	// CreatedAt / UpdatedAt RFC3339Nano(UTC)。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (AutomationOutboxEntity) TableName() string { return TableNameAutomationOutbox }

// AutomationOutboxEntityList 是 AutomationOutboxEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AutomationOutboxEntityList []AutomationOutboxEntity
