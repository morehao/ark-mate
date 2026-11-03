package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LibraryAttachmentCond 资料库节点↔附件关联查询条件。
type LibraryAttachmentCond struct {
	*gormdao.BaseCond
	NodeID       string
	AttachmentID string
	Role         string
}

// BuildCondition 实现 gormdao.Cond。
func (c *LibraryAttachmentCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.NodeID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "node_id"}, Value: c.NodeID})
	}
	if c.AttachmentID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "attachment_id"}, Value: c.AttachmentID})
	}
	if c.Role != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "role"}, Value: c.Role})
	}
}

// LibraryAttachmentDao 访问 library_attachments 表（节点↔附件关联）。
//
// **复合主键 `(node_id, attachment_id)`**：gormdao 的 `Delete`/`UpdateByID`/`GetByID` 都以单列
// `id = ?` 定位，**不适用**本表。读写用 `Insert`/`GetListByCond`/`GetByCond`；按复合键删用
// `DB(ctx)` 直接 `Where(node_id, attachment_id).Delete(...)`（业务侧封装在 svcmaterial）。
type LibraryAttachmentDao struct {
	*gormdao.Dao[model.LibraryAttachmentEntity, model.LibraryAttachmentEntityList, string]
}

// NewLibraryAttachmentDao 构造 LibraryAttachmentDao。
func NewLibraryAttachmentDao(getDB gormdao.DBGetter) *LibraryAttachmentDao {
	return &LibraryAttachmentDao{Dao: newDao[model.LibraryAttachmentEntity, model.LibraryAttachmentEntityList, string]("LibraryAttachmentDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *LibraryAttachmentDao) WithTx(tx *gorm.DB) *LibraryAttachmentDao {
	return &LibraryAttachmentDao{Dao: d.Dao.WithTx(tx)}
}
