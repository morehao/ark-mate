package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LibraryTableCond 资料库 table 节点查询条件。
type LibraryTableCond struct {
	*gormdao.BaseCond
	NodeID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *LibraryTableCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.NodeID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "node_id"}, Value: c.NodeID})
	}
}

// LibraryTableDao 访问 library_tables 表（table 节点的 schema + 行）。
//
// 主键是 `node_id`（不是 `id`）：故 `GetByID`/`Delete`/`UpdateByID`（gormdao 用单列 `id = ?`）**不适用**。
// 读写用 `Insert`/`GetByCond`/`GetByCond` 更新走 `UpdateMap` 亦不可（按 id）——统一用 `GetByCond`
// 读、`DB(ctx).Model(...).Where("node_id = ?").Updates(...)` 写（业务侧封装在 svcmaterial）。
type LibraryTableDao struct {
	*gormdao.Dao[model.LibraryTableEntity, model.LibraryTableEntityList, string]
}

// NewLibraryTableDao 构造 LibraryTableDao。
func NewLibraryTableDao(getDB gormdao.DBGetter) *LibraryTableDao {
	return &LibraryTableDao{Dao: newDao[model.LibraryTableEntity, model.LibraryTableEntityList, string]("LibraryTableDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *LibraryTableDao) WithTx(tx *gorm.DB) *LibraryTableDao {
	return &LibraryTableDao{Dao: d.Dao.WithTx(tx)}
}
