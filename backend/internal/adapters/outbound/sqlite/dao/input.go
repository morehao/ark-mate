package dao

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InputCond 队列项查询条件。
type InputCond struct {
	*gormdao.BaseCond
	SessionID string
	Status    string
}

// BuildCondition 实现 gormdao.Cond。
func (c *InputCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
	if c.Status != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "status"}, Value: c.Status})
	}
}

// InputDao 访问 inputs 表（§6 busy input 队列）。
//
// 本 DAO 只提供列词汇（`InputCond`）与泛型方法；取号、队首、状态迁移、重排的两阶段
// 都是队列的业务口径，住在 `internal/adapters/outbound/sqlite/store/inputs.go`。
type InputDao struct {
	*gormdao.Dao[model.InputEntity, model.InputEntityList, string]
}

// NewInputDao 构造 InputDao（getDB 必填，见包注释）。
func NewInputDao(getDB gormdao.DBGetter) *InputDao {
	return &InputDao{Dao: newDao[model.InputEntity, model.InputEntityList, string]("InputDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *InputDao) WithTx(tx *gorm.DB) *InputDao {
	return &InputDao{Dao: d.Dao.WithTx(tx)}
}
