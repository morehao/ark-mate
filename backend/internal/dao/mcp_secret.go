package dao

import (
	"context"
	"errors"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// McpSecretDao 访问 mcp_secrets 表（v20：引用串 → 明文值）。
type McpSecretDao struct {
	*gormdao.Dao[model.McpSecretEntity, model.McpSecretEntityList, string]
}

// NewMcpSecretDao 构造 McpSecretDao。
func NewMcpSecretDao(getDB gormdao.DBGetter) *McpSecretDao {
	return &McpSecretDao{Dao: newDao[model.McpSecretEntity, model.McpSecretEntityList, string]("McpSecretDao", getDB)}
}

// Get 读一个引用串的明文值；不存在返回 (nil, false, nil)。
//
// 不用 gormdao 的 GetByID：它把主键列硬编码成 `id`，而本表主键列是 `ref`（同 MetaDao）。
func (d *McpSecretDao) Get(ctx context.Context, ref string) (*model.McpSecretEntity, bool, error) {
	var row model.McpSecretEntity
	err := d.DB(ctx).Where("ref = ?", ref).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &row, true, nil
}

// List 列出全部密钥（ref 升序；doctor 的孤儿判定要这份全量）。
func (d *McpSecretDao) List(ctx context.Context) (model.McpSecretEntityList, error) {
	var rows model.McpSecretEntityList
	err := d.DB(ctx).Order("ref ASC").Find(&rows).Error
	return rows, err
}

// Set 写入（存在则覆盖），覆盖时 revision +1、updated_at 跟着走。
//
// revision 单调是为了让"这个引用变过几次"可解释；覆盖而不是删了重建，是为了让
// 一条被清空的值（空串）也留下痕迹——"清掉密钥"与"从来没有过"在库里应当可分。
func (d *McpSecretDao) Set(ctx context.Context, ref, value string, at time.Time) error {
	return d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ref"}},
		DoUpdates: clause.Assignments(map[string]any{
			"value":      value,
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": at,
		}),
	}).Create(&model.McpSecretEntity{Ref: ref, Value: value, Revision: 1, UpdatedAt: at}).Error
}

// Delete 删除一个引用串，返回是否真删掉了一行。
func (d *McpSecretDao) Delete(ctx context.Context, ref string) (bool, error) {
	res := d.DB(ctx).Where("ref = ?", ref).Delete(&model.McpSecretEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Count 现有行数（doctor 的规模事实）。
func (d *McpSecretDao) Count(ctx context.Context) (int64, error) {
	var n int64
	err := d.DB(ctx).Model(&model.McpSecretEntity{}).Count(&n).Error
	return n, err
}
