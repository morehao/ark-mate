package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// DefinitionRevisionDao 访问 definition_revisions 表（定义变更审计；v16 前叫 capability_revisions）。
//
// 本表**没有 `id` 列**（主键叫 `seq`），因此 gormdao 那批以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用，只用下面这些自定义方法
// （同 `meta` / `events` / `schema_migrations`，见包注释）。
type DefinitionRevisionDao struct {
	*gormdao.Dao[model.DefinitionRevisionEntity, model.DefinitionRevisionEntityList, int64]
}

// NewDefinitionRevisionDao 构造 DefinitionRevisionDao（getDB 必填，见包注释）。
func NewDefinitionRevisionDao(getDB gormdao.DBGetter) *DefinitionRevisionDao {
	return &DefinitionRevisionDao{Dao: newDao[model.DefinitionRevisionEntity, model.DefinitionRevisionEntityList, int64]("DefinitionRevisionDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *DefinitionRevisionDao) WithTx(tx *gorm.DB) *DefinitionRevisionDao {
	return &DefinitionRevisionDao{Dao: d.Dao.WithTx(tx)}
}

// Append 追一条审计。审计是**只追加**的：不提供更新与删除。
//
// 刻意不显式写入 seq：留空（零值）由 SQLite 的 AUTOINCREMENT 分配，这样"分配序号"与
// "插行"是同一个原子动作，并发下不可能出现两条相同的 seq，也不可能出现空洞被复用。
func (d *DefinitionRevisionDao) Append(ctx context.Context, rev *model.DefinitionRevisionEntity) error {
	return d.DB(ctx).Omit("seq").Create(rev).Error
}

// MaxSeq 返回当前最大序号（无记录 → 0）。它就是"M1 落地时代际 rev 的取值源"。
func (d *DefinitionRevisionDao) MaxSeq(ctx context.Context) (int64, error) {
	var seq int64
	// COALESCE：空表时 MAX 返回 NULL，直接扫进 int64 会报错。
	err := d.DB(ctx).Raw(`SELECT COALESCE(MAX(seq), 0) FROM ` + model.TableNameDefinitionRevision).Scan(&seq).Error
	return seq, err
}

// ListByDefinition 列出某条定义（`kind` 限定域）的变更历史（seq 升序）。
func (d *DefinitionRevisionDao) ListByDefinition(ctx context.Context, kind, definitionID string) (model.DefinitionRevisionEntityList, error) {
	var rows model.DefinitionRevisionEntityList
	err := d.DB(ctx).
		Where("kind = ? AND definition_id = ?", kind, definitionID).
		Order("seq ASC").Find(&rows).Error
	return rows, err
}

// ListRecent 列出最近 limit 条变更（seq 降序；limit <= 0 视为 50）。
func (d *DefinitionRevisionDao) ListRecent(ctx context.Context, limit int) (model.DefinitionRevisionEntityList, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows model.DefinitionRevisionEntityList
	err := d.DB(ctx).Order("seq DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
