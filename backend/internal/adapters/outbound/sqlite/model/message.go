package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameMessage 消息物化投影（可从 events 重建）。
	TableNameMessage = "messages"
)

// MessageEntity 是 messages 表的一行：**白名单投影**的一行对话正文（v23 起）。
//
// "白名单"= 只有 `session.user_message` 与 `session.message` 两类事件成行（其余事件留在
// events 里，由事件读面渲染时间线）；"空正文不产行"。整会话可从 events 重铺
// （`session.Plane.RebuildSessionReadModels`），`ordinal` 是重铺后的连续序号。
type MessageEntity struct {
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	RunID     string `gorm:"column:run_id;not null"`
	// Role 消息角色。取值见 RoleUser / RoleAssistant。
	Role            string `gorm:"column:role;not null"`
	Text            string `gorm:"column:text;not null"`
	EventsJSON      string `gorm:"column:events_json;not null"`
	ToolDetailsJSON string `gorm:"column:tool_details_json;not null"`
	// Ordinal 会话内第几条消息（1 起、稠密、**唯一**：`UNIQUE(session_id, ordinal)`，v23）。
	// 它是"投影完不完整"的载体（序号有洞 = 少了一行；重号 = 多了一行），也是给人看的稳定序号
	// ——位次（`seq_lo`）在 v21 换过一次口径，而"第几条"不该跟着口径变。
	Ordinal int64 `gorm:"column:ordinal;not null"`
	// Kind 投影行的种类（扩展位，v23）。当前**恒为空串**：只有将来真要多一种"可搜事实"时才取值
	// （J5 一列一义——不给"以后可能有用"留半个含义）。
	Kind      string    `gorm:"column:kind;not null"`
	SeqLo     int64     `gorm:"column:seq_lo"`
	SeqHi     int64     `gorm:"column:seq_hi"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (MessageEntity) TableName() string { return TableNameMessage }

// MessageEntityList 是 MessageEntity 切片类型（gormdao 泛型参数要求具名切片）。
type MessageEntityList []MessageEntity
