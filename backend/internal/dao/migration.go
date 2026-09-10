package dao

import (
	"context"
	"time"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// MigrationDao 访问 schema_migrations 表。
//
// 表的创建与应用不在这里（见 internal/store 的迁移内核）：内核要保持
// 「有序 + 单事务 DDL + 版本拒绝启动 + staging 回滚」这些 gorm AutoMigrate
// 表达不了的语义，本 DAO 只负责内核自身的读写。
type MigrationDao struct {
	*gormdao.Dao[model.Migration, model.MigrationList, int64]
}

// NewMigrationDao 构造 MigrationDao（getDB 必填，见包注释）。
func NewMigrationDao(getDB gormdao.DBGetter) *MigrationDao {
	return &MigrationDao{Dao: newDao[model.Migration, model.MigrationList, int64]("MigrationDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *MigrationDao) WithTx(tx *gorm.DB) *MigrationDao {
	return &MigrationDao{Dao: d.Dao.WithTx(tx)}
}

// AppliedVersions 返回已应用的版本集合与最大版本（空库 → 空集合 + 0）。
func (d *MigrationDao) AppliedVersions(ctx context.Context) (map[int]bool, int, error) {
	list, err := d.GetListByCond(ctx, &gormdao.BaseCond{OrderField: "version ASC"})
	if err != nil {
		return nil, 0, err
	}
	applied := make(map[int]bool, len(list))
	maxVersion := 0
	for _, m := range list {
		applied[m.Version] = true
		if m.Version > maxVersion {
			maxVersion = m.Version
		}
	}
	return applied, maxVersion, nil
}

// Record 记录一个已应用的版本（在迁移事务内调用）。
func (d *MigrationDao) Record(ctx context.Context, version int, name string) error {
	return d.Insert(ctx, &model.Migration{Version: version, Name: name, AppliedAt: time.Now().UTC()})
}
