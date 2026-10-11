package dao

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MetaDao 访问 meta 表（任意键值：全局序号计数、导入标记等）。
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
// 主键列不叫 id 的实体（meta.key、events.seq、schema_migrations.version）只能走自定义方法。
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

// IncrSeq 在事务内把全局事件序号 +1 并返回新值（events.seq 的唯一分配源）。
//
// 必须是事务（调用方持有 tx 并已用 WithTx 绑定）：本仓是单写者模型，
// 序号分配与事件插入必须原子，否则并发 turn 会拿到重号。
func (d *MetaDao) IncrSeq(ctx context.Context) (int64, error) {
	cur, ok, err := d.Get(ctx, model.MetaKeyEventsLastSeq)
	if err != nil {
		return 0, err
	}
	var next int64
	if ok {
		parsed, perr := strconv.ParseInt(cur, 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("dao: meta %s 值损坏 %q: %w", model.MetaKeyEventsLastSeq, cur, perr)
		}
		next = parsed
	}
	next++
	if err := d.Set(ctx, model.MetaKeyEventsLastSeq, strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	return next, nil
}

// WithTx 返回绑定事务的新 DAO（事务内调用不得脱离 tx，否则破坏单写者语义）。
func (d *MetaDao) WithTx(tx *gorm.DB) *MetaDao {
	return &MetaDao{Dao: d.Dao.WithTx(tx)}
}
