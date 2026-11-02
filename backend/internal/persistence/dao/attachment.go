package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AttachmentCond 附件查询条件。
//
// 列词汇只有这几个「同一表上会用来筛选」的键；排序 / 分页 / 组合查询归
// `internal/persistence/store`（dao 只留列词汇 + 泛型方法 + 机械原语，见 guards.md 的 v3.3）。
type AttachmentCond struct {
	*gormdao.BaseCond
	SessionID string
	State     string
}

// BuildCondition 实现 gormdao.Cond。
func (c *AttachmentCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
	if c.State != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "state"}, Value: c.State})
	}
}

// AttachmentDao 访问 attachments 表（平台附件业务索引）。
//
// 本 DAO 只提供列词汇（`AttachmentCond`）与 gormdao 的泛型方法；「附件行 ↔ filestore upload 行」
// 的一致性、孤儿扫描、挂载状态迁移等业务口径住在 `internal/persistence/store/attachments.go`。
type AttachmentDao struct {
	*gormdao.Dao[model.AttachmentEntity, model.AttachmentEntityList, string]
}

// NewAttachmentDao 构造 AttachmentDao（getDB 必填，见包注释）。
func NewAttachmentDao(getDB gormdao.DBGetter) *AttachmentDao {
	return &AttachmentDao{Dao: newDao[model.AttachmentEntity, model.AttachmentEntityList, string]("AttachmentDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *AttachmentDao) WithTx(tx *gorm.DB) *AttachmentDao {
	return &AttachmentDao{Dao: d.Dao.WithTx(tx)}
}
