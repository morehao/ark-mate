package dao

import (
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SessionCond 会话查询条件。
type SessionCond struct {
	*gormdao.BaseCond
	// Archived 为 nil 表示不过滤归档状态。
	Archived *bool
}

// BuildCondition 实现 gormdao.Cond。
func (c *SessionCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.Archived != nil {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "archived"}, Value: model.FlagValue(*c.Archived)})
	}
}

// SessionDao 访问 sessions 表（会话权威元数据）。
//
// 本 DAO 只提供列词汇（`SessionCond`）与 gormdao 的泛型方法；排序、覆盖语义（哪些列在冲突时保留）、
// 水位推进入口都在 `internal/store/sessions.go`——那是会话的业务口径。
type SessionDao struct {
	*gormdao.Dao[model.SessionEntity, model.SessionEntityList, string]
}

// NewSessionDao 构造 SessionDao（getDB 必填，见包注释）。
func NewSessionDao(getDB gormdao.DBGetter) *SessionDao {
	return &SessionDao{Dao: newDao[model.SessionEntity, model.SessionEntityList, string]("SessionDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *SessionDao) WithTx(tx *gorm.DB) *SessionDao {
	return &SessionDao{Dao: d.Dao.WithTx(tx)}
}
