package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LibraryNodeCond 资料库节点查询条件。
//
// 列词汇只留「同一表上会用来筛选」的键；排序 / 分页 / 关键词 LIKE 组合归
// `internal/persistence/store` 或用例层（dao 只留列词汇 + 泛型方法 + 机械原语）。
type LibraryNodeCond struct {
	*gormdao.BaseCond
	SpaceID  string
	ParentID string
	NodeKind string
}

// BuildCondition 实现 gormdao.Cond。
func (c *LibraryNodeCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SpaceID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "space_id"}, Value: c.SpaceID})
	}
	if c.ParentID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "parent_id"}, Value: c.ParentID})
	}
	if c.NodeKind != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "node_kind"}, Value: c.NodeKind})
	}
}

// LibraryNodeDao 访问 library_nodes 表（资料库目录树）。
//
// 本 DAO 只提供列词汇（`LibraryNodeCond`）与 gormdao 的泛型方法；「成环校验、级联删子树、
// 附件挂接、关键词检索」等业务口径住在用例层（资料库 service）。
type LibraryNodeDao struct {
	*gormdao.Dao[model.LibraryNodeEntity, model.LibraryNodeEntityList, string]
}

// NewLibraryNodeDao 构造 LibraryNodeDao（getDB 必填，见包注释）。
func NewLibraryNodeDao(getDB gormdao.DBGetter) *LibraryNodeDao {
	return &LibraryNodeDao{Dao: newDao[model.LibraryNodeEntity, model.LibraryNodeEntityList, string]("LibraryNodeDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *LibraryNodeDao) WithTx(tx *gorm.DB) *LibraryNodeDao {
	return &LibraryNodeDao{Dao: d.Dao.WithTx(tx)}
}
