package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// CapabilityRevisionDao 访问 capability_revisions 表（能力定义变更审计）。
//
// 本表**没有 `id` 列**（主键叫 `seq`），因此 gormdao 那批以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用，只用下面这些自定义方法
// （同 `meta` / `events` / `schema_migrations`，见包注释）。
type CapabilityRevisionDao struct {
	*gormdao.Dao[model.CapabilityRevision, model.CapabilityRevisionList, int64]
}

// NewCapabilityRevisionDao 构造 CapabilityRevisionDao（getDB 必填，见包注释）。
func NewCapabilityRevisionDao(getDB gormdao.DBGetter) *CapabilityRevisionDao {
	return &CapabilityRevisionDao{Dao: newDao[model.CapabilityRevision, model.CapabilityRevisionList, int64]("CapabilityRevisionDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *CapabilityRevisionDao) WithTx(tx *gorm.DB) *CapabilityRevisionDao {
	return &CapabilityRevisionDao{Dao: d.Dao.WithTx(tx)}
}

// Append 追一条审计。审计是**只追加**的：不提供更新与删除。
//
// 刻意不显式写入 seq：留空（零值）由 SQLite 的 AUTOINCREMENT 分配，这样"分配序号"与
// "插行"是同一个原子动作，并发下不可能出现两条相同的 seq，也不可能出现空洞被复用。
func (d *CapabilityRevisionDao) Append(ctx context.Context, rev *model.CapabilityRevision) error {
	return d.DB(ctx).Omit("seq").Create(rev).Error
}

// MaxSeq 返回当前最大序号（无记录 → 0）。它就是"M1 落地时代际 rev 的取值源"。
func (d *CapabilityRevisionDao) MaxSeq(ctx context.Context) (int64, error) {
	var seq int64
	// COALESCE：空表时 MAX 返回 NULL，直接扫进 int64 会报错。
	err := d.DB(ctx).Raw(`SELECT COALESCE(MAX(seq), 0) FROM ` + model.TableCapabilityRevision).Scan(&seq).Error
	return seq, err
}

// ListByCapability 列出某条能力的变更历史（seq 升序）。
func (d *CapabilityRevisionDao) ListByCapability(ctx context.Context, kind, capabilityID string) (model.CapabilityRevisionList, error) {
	var rows model.CapabilityRevisionList
	err := d.DB(ctx).
		Where("kind = ? AND capability_id = ?", kind, capabilityID).
		Order("seq ASC").Find(&rows).Error
	return rows, err
}

// ListRecent 列出最近 limit 条变更（seq 降序；limit <= 0 视为 50）。
func (d *CapabilityRevisionDao) ListRecent(ctx context.Context, limit int) (model.CapabilityRevisionList, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows model.CapabilityRevisionList
	err := d.DB(ctx).Order("seq DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
