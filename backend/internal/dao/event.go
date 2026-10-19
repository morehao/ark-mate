package dao

import (
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventCond 是 events 表的**非游标**条件（会话维度）。
//
// events 自 v17 起主键列就叫 `id`，按行身份取一条直接走 GetByID；**游标**（`seq` 区间 + 多取
// 一条判 hasMore）不进 Cond——它是读面的翻页语义，住在 `internal/store/events.go`。
type EventCond struct {
	*gormdao.BaseCond
	SessionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *EventCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
}

// EventDao 访问 events 表（facts 的唯一事实源）。
//
// 泛型 ID 自 v17 起是 `string`（行身份 `evt_…`）。本 DAO 只提供列词汇（`EventCond`）与
// gormdao 的泛型方法；**游标只认 `seq`** 这条读面口径、以及"多取一条判 hasMore"的翻页实现，
// 都在 `internal/store/events.go`（守卫 G1 钉着"按 id 排序/比较"的禁令，指向那里）。
type EventDao struct {
	*gormdao.Dao[model.EventEntity, model.EventEntityList, string]
}

// NewEventDao 构造 EventDao（getDB 必填，见包注释）。
func NewEventDao(getDB gormdao.DBGetter) *EventDao {
	return &EventDao{Dao: newDao[model.EventEntity, model.EventEntityList, string]("EventDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO（序号分配与事件插入必须同事务）。
func (d *EventDao) WithTx(tx *gorm.DB) *EventDao {
	return &EventDao{Dao: d.Dao.WithTx(tx)}
}
