package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameMessage 消息物化投影（可从 events 重建）。
	TableNameMessage = "messages"
)

// MessageEntity 是 messages 表的一行（消息物化：role/text + 交错时间线 + 工具最终态）。
type MessageEntity struct {
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	RunID     string `gorm:"column:run_id;not null"`
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
func (MessageEntity) TableName() string { return TableNameMessage }

// MessageEntityList 是 MessageEntity 切片类型（gormdao 泛型参数要求具名切片）。
type MessageEntityList []MessageEntity
