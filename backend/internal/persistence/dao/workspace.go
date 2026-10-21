package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WorkspaceCond 是 workspaces 表的查询条件（登记路径 / 默认档两个查询轴）。
//
// 两个字段都是可选的：按路径取一行只给 Path，取默认档只给 IsDefault。
// IsDefault 用 *bool 而不是 bool：false 也是**有效筛选值**（"非默认的那几条"），
// 零值语义会让它退化成"不过滤"。
type WorkspaceCond struct {
	*gormdao.BaseCond
	Path      string
	IsDefault *bool
}

// BuildCondition 实现 gormdao.Cond。
func (c *WorkspaceCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.Path != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "path"}, Value: c.Path})
	}
	if c.IsDefault != nil {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "is_default"}, Value: model.FlagValue(*c.IsDefault)})
	}
}

// WorkspaceDao 访问 workspaces 表（工作空间登记；v14）。
//
// 本 DAO **只提供列词汇**（`WorkspaceCond`）与 gormdao 的泛型方法；"展示顺序是什么"、
// "什么是默认档"、"新登记排在哪"这些**业务口径归 `internal/persistence/store`**——那里的
// `ListWorkspaces`/`DefaultWorkspace`/`CreateWorkspace` 用 Cond + 泛型方法拼出来。
type WorkspaceDao struct {
	*gormdao.Dao[model.WorkspaceEntity, model.WorkspaceEntityList, string]
}

// NewWorkspaceDao 构造 WorkspaceDao（getDB 必填，见包注释）。
func NewWorkspaceDao(getDB gormdao.DBGetter) *WorkspaceDao {
	return &WorkspaceDao{Dao: newDao[model.WorkspaceEntity, model.WorkspaceEntityList, string]("WorkspaceDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *WorkspaceDao) WithTx(tx *gorm.DB) *WorkspaceDao {
	return &WorkspaceDao{Dao: d.Dao.WithTx(tx)}
}
