package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ArtifactCond 产物查询条件（列词汇）。
//
// 排序 / 分页等组合归 `internal/persistence/store` 或用例层（dao 只留列词汇 + 泛型方法 + 机械原语）。
type ArtifactCond struct {
	*gormdao.BaseCond
	SessionID string
	RunID     string
}

// BuildCondition 实现 gormdao.Cond。
func (c *ArtifactCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
	if c.RunID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "run_id"}, Value: c.RunID})
	}
}

// ArtifactDao 访问 artifacts 表（产物溯源登记）。
type ArtifactDao struct {
	*gormdao.Dao[model.ArtifactEntity, model.ArtifactEntityList, string]
}

// NewArtifactDao 构造 ArtifactDao（getDB 必填，见包注释）。
func NewArtifactDao(getDB gormdao.DBGetter) *ArtifactDao {
	return &ArtifactDao{Dao: newDao[model.ArtifactEntity, model.ArtifactEntityList, string]("ArtifactDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *ArtifactDao) WithTx(tx *gorm.DB) *ArtifactDao {
	return &ArtifactDao{Dao: d.Dao.WithTx(tx)}
}
