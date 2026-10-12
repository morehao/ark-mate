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
	//
	// **events 刻意没有另外一支 `id` 主键**（v16 / D6 的结论，2026-10-11）：seq 本身就是
	// "有消费者的稳定身份"——对外帧 `{type,seq,agg_id,version,payload}` 是它、续传锚点
	// `since_seq` 是它、会话水位 `sessions.last_seq` 也是它；再加一支 `evt_<ULID>` 只会
	// 表达同一件事的第二份值（objid 的字典序 = 铸造序），而且**一个消费者都没有**
	// （本仓自己的代码里 `grep -rn event_id` 零命中——随包底座载荷的 node_modules 里有，
	// 那是别人 SDK 的字段；也没有 `GET /events/:id`）。
	// 判据一句话：**会被单独引用 → 给 id；只被按序消费 → 用游标**——对照 messages：
	// 它要被 `session.message` 增量按 msg_id 单独追加，所以它有 `msg_` id。
	//
	// 代价如实记：seq 是**全局**的，所以单个会话内的 seq **有洞**（会话 A 可能是 1、5、9…）。
	// 所有现有消费者都按 `> since_seq` 比较、不要求稠密，因此今天无害；将来真要"会话内稠密
	// 序号"，正确做法是加 `(session_id, seq)` 复合身份或另立会话内序号列，**不是**加一支
	// 与排序无关的 id。
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
