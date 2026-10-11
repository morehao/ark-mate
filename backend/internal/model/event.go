package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameEvent 事件事实源（唯一写入口见 store.AppendEvent）。
	TableNameEvent = "events"
)

// EventEntity 是 events 表的一行（facts 的唯一事实源）。
type EventEntity struct {
	// Seq 全局单调序号（meta.events_last_seq 是唯一分配源，见 store 的分配事务）。
	Seq       int64  `gorm:"column:seq;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	// Type 事件类型。取值权威定义在 internal/manager/events.go。
	Type string `gorm:"column:type;not null"`
	// PayloadJSON 事件载荷（一律序列化为 JSON 文本，含结构体载荷）。
	PayloadJSON string    `gorm:"column:payload_json;not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (EventEntity) TableName() string { return TableNameEvent }

// EventEntityList 是 EventEntity 切片类型（gormdao 泛型参数要求具名切片）。
type EventEntityList []EventEntity
