package dao

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MetaDao 访问 meta 表（任意键值：导入标记、目录级偏好、读模型重建标记等）。
//
// v21 起**不再**承载事件号序：那是每会话一行的 `event_sequence`
// （`EventSequenceDao.NextSeq`）。本表的键都是"目录/进程级"的事实，没有一个会随会话增长。
type MetaDao struct {
	*gormdao.Dao[model.MetaEntity, model.MetaEntityList, string]
}

// NewMetaDao 构造 MetaDao（getDB 必填，见包注释）。
func NewMetaDao(getDB gormdao.DBGetter) *MetaDao {
	return &MetaDao{Dao: newDao[model.MetaEntity, model.MetaEntityList, string]("MetaDao", getDB)}
}

// Get 读取任意键；不存在返回 ("", false, nil)（不存在不是错误）。
//
// 不用 gormdao 的 GetByID：它把主键列硬编码成 `id`，而 meta 表的主键列是 `key`。
// 泛型 Dao 中所有按主键操作的方法（GetByID/UpdateByID/UpdateMap/Delete）都有同样的假设，
// 主键列不叫 id 的实体（meta.key、schema_migrations.version）只能走自定义方法。
func (d *MetaDao) Get(ctx context.Context, key string) (string, bool, error) {
	var row model.MetaEntity
	err := d.DB(ctx).Model(&model.MetaEntity{}).Where("key = ?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Value, true, nil
}

// Set 写入任意键（存在则覆盖）。
func (d *MetaDao) Set(ctx context.Context, key, value string) error {
	return d.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.MetaEntity{Key: key, Value: value}).Error
}

// Delete 删任意键（不存在 → 无错，幂等）。与 Get 同一条理由：gormdao 的 `Delete` 按硬编码的
// `id` 列定位，对主键列叫 `key` 的本表不适用。
func (d *MetaDao) Delete(ctx context.Context, key string) error {
	return d.DB(ctx).Where("key = ?", key).Delete(&model.MetaEntity{}).Error
}

// WithTx 返回绑定事务的新 DAO（事务内调用不得脱离 tx，否则破坏单写者语义）。
func (d *MetaDao) WithTx(tx *gorm.DB) *MetaDao {
	return &MetaDao{Dao: d.Dao.WithTx(tx)}
}
