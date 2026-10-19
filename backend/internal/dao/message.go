package dao

import (
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MessageCond 消息查询条件。
type MessageCond struct {
	*gormdao.BaseCond
	SessionID string
	RunID     string
	Role      string
}

// BuildCondition 实现 gormdao.Cond。
func (c *MessageCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
	if c.RunID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "run_id"}, Value: c.RunID})
	}
	if c.Role != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "role"}, Value: c.Role})
	}
}

// MessageDao 访问 messages 表（消息物化投影，可从 events 重建）。
//
// 本 DAO 只提供列词汇（`MessageCond`）与 gormdao 的泛型方法；"整会话先删后插"、
// "按 seq_lo 排"、正文检索的转义与 JOIN 都在 `internal/store/messages.go`。
type MessageDao struct {
	*gormdao.Dao[model.MessageEntity, model.MessageEntityList, string]
}

// NewMessageDao 构造 MessageDao（getDB 必填，见包注释）。
func NewMessageDao(getDB gormdao.DBGetter) *MessageDao {
	return &MessageDao{Dao: newDao[model.MessageEntity, model.MessageEntityList, string]("MessageDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *MessageDao) WithTx(tx *gorm.DB) *MessageDao {
	return &MessageDao{Dao: d.Dao.WithTx(tx)}
}
