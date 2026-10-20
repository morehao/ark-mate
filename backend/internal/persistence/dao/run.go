package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RunCond 轮次查询条件。
type RunCond struct {
	*gormdao.BaseCond
	SessionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *RunCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
}

// RunDao 访问 runs 表（每轮 prompt 的物化行）。
//
// 本 DAO 只提供列词汇（`RunCond`）与泛型方法；开轮幂等、"进行中"兜底、收口写入、
// 用量聚合的输入读面都在 `internal/persistence/store/runs.go`。
type RunDao struct {
	*gormdao.Dao[model.RunEntity, model.RunEntityList, string]
}

// NewRunDao 构造 RunDao（getDB 必填，见包注释）。
func NewRunDao(getDB gormdao.DBGetter) *RunDao {
	return &RunDao{Dao: newDao[model.RunEntity, model.RunEntityList, string]("RunDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *RunDao) WithTx(tx *gorm.DB) *RunDao {
	return &RunDao{Dao: d.Dao.WithTx(tx)}
}
