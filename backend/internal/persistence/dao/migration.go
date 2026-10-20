package dao

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// SchemaMigrationDao 访问 schema_migrations 表。
//
// 表的创建与应用不在这里（见 internal/persistence/store 的迁移内核）：内核要保持
// 「有序 + 单事务 DDL + 版本拒绝启动 + staging 回滚」这些 gorm AutoMigrate
// 表达不了的语义，本 DAO 只负责**写入一本账**（读面/排序由 store 的迁移内核表达）。
type SchemaMigrationDao struct {
	*gormdao.Dao[model.SchemaMigrationEntity, model.SchemaMigrationEntityList, int64]
}

// NewMigrationDao 构造 SchemaMigrationDao（getDB 必填，见包注释）。
func NewMigrationDao(getDB gormdao.DBGetter) *SchemaMigrationDao {
	return &SchemaMigrationDao{Dao: newDao[model.SchemaMigrationEntity, model.SchemaMigrationEntityList, int64]("SchemaMigrationDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *SchemaMigrationDao) WithTx(tx *gorm.DB) *SchemaMigrationDao {
	return &SchemaMigrationDao{Dao: d.Dao.WithTx(tx)}
}

// Record 记录一个已应用的版本（在迁移事务内调用）。
func (d *SchemaMigrationDao) Record(ctx context.Context, version int, name string) error {
	return d.Insert(ctx, &model.SchemaMigrationEntity{Version: version, Name: name, AppliedAt: time.Now().UTC()})
}
