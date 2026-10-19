package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MetaCond 是 meta 表的查询条件。
//
// 本表主键列叫 `key`（不是 `id`），所以不能用 BaseCond.ID：那个字段固定生成
// `<table>.id = ?`。条件查询统一走 GetByCond，键列由这里给出。
//
// `Key` **总是**参与筛选（空串就是查空键，与自定义查询时期逐字一致）——
// 它没有"空 = 不过滤"的退化：那样 `Get(ctx, "")` 会静默返回全表第一行。
type MetaCond struct {
	*gormdao.BaseCond
	Key string
}

// BuildCondition 实现 gormdao.Cond。
func (c *MetaCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "key"}, Value: c.Key})
}

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
// 用 GetByCond 而不是 GetByID：后者把主键列硬编码成 `id`，而本表主键列是 `key`。
// 查询本身（LIMIT 1、错误归一）仍由 gormdao 提供。
func (d *MetaDao) Get(ctx context.Context, key string) (string, bool, error) {
	row, err := d.GetByCond(ctx, &MetaCond{Key: key})
	if err != nil {
		return "", false, err
	}
	if row == nil {
		return "", false, nil
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
