package dao

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RowDao 是**资源表**（列 + revision CAS）的共用实现：v13 起 `providers` / `models` 用它。
//
// 与 `DefinitionDao` 的分工是判据层面的，不是历史偶然（ADR-2）：
//
//	DefinitionDao：`body` 是唯一权威（正文是**用户自由文档**）→ skills / mcp_servers / agents / automations；
//	RowDao       ：平台拥有的字段**全部是列**（键集合封闭）→ providers / models。
//
// 为什么另起一个泛型而不是给 `UpdateCAS` 加开关：`DefinitionDao.UpdateCAS` 的 `body string`
// 是**方法签名的一部分**，列化后那一套根本复用不了；同构的泛型比"一个方法两种语义"干净。
//
// **CAS 判据只有一份实现**：`rowsAffected == 0` 就是 `definition_changed`(40901) 的唯一来源。
// 两套 DAO 的 `DeleteCAS` 判据（`id + revision`）逐字相同——两边各写一遍判据，迟早出现
// "一张表挡住并发、另一张放过"的静默分叉（设计「风险评估」第 2 行）。
//
// 读面（取行/列表/计数）由 store 用泛型方法直接表达；本 DAO 只保留 CAS 写原语。
type RowDao[T gormdao.Entity, L ~[]T] struct {
	*gormdao.Dao[T, L, string]
}

// newRowDao 构造共用 DAO（getDB 必填，见包注释）。
func newRowDao[T gormdao.Entity, L ~[]T](daoName string, getDB gormdao.DBGetter) *RowDao[T, L] {
	return &RowDao[T, L]{Dao: newDao[T, L, string](daoName, getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *RowDao[T, L]) WithTx(tx *gorm.DB) *RowDao[T, L] {
	return &RowDao[T, L]{Dao: d.Dao.WithTx(tx)}
}

// CreateCAS 新建一行——主键冲突时**不报错**而是返回 false（调用方据此回 40901，
// 与"别人先建了"是同一个对外表现）。revision 一律从 1 起，不接受调用方传入。
func (d *RowDao[T, L]) CreateCAS(ctx context.Context, row *T) (bool, error) {
	res := d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateColumnsCAS 覆盖一行的**内容列**，仅当 revision 仍等于 expectedRev。
//
// 返回 false = 有人在这中间改过（或行已不存在）：调用方把它翻成 `definition_changed`(40901)。
// 条件更新是并发下的唯一仲裁者，没有旁路（这里刻意不做"先读后写"）。
//
// `columns` 是**内容列**（如 name/note/input_modalities）：身份列与记账列由本方法自己钉住——
// `id` 不进 SET，`revision`/`updated_at` 由 CAS 语义决定。传进来也不生效（防御性剔除，
// 免得将来有人以为"传了 id 就能改主键"）。
func (d *RowDao[T, L]) UpdateColumnsCAS(ctx context.Context, id string, columns map[string]any, expectedRev int64, now time.Time) (bool, error) {
	updates := make(map[string]any, len(columns)+2)
	for k, v := range columns {
		switch k {
		case "id", "revision", "created_at", "updated_at":
			continue
		}
		updates[k] = v
	}
	updates["revision"] = expectedRev + 1
	updates["updated_at"] = model.FormatTimeOrNow(now)
	res := d.DB(ctx).Model(new(T)).
		Where("id = ? AND revision = ?", id, expectedRev).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteCAS 删除一行，**仅当 revision 仍等于 expectedRev**；返回是否删掉。
func (d *RowDao[T, L]) DeleteCAS(ctx context.Context, id string, expectedRev int64) (bool, error) {
	res := d.DB(ctx).Where("id = ? AND revision = ?", id, expectedRev).Delete(new(T))
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ——— 具体 DAO（调用方只认它们，泛型参数在这里被钉死） ———

// ProviderDao 访问 providers 表（v13：列 + revision CAS）。
type ProviderDao struct {
	*RowDao[model.ProviderEntity, model.ProviderEntityList]
}

// NewProviderDao 构造 ProviderDao。
func NewProviderDao(getDB gormdao.DBGetter) *ProviderDao {
	return &ProviderDao{RowDao: newRowDao[model.ProviderEntity, model.ProviderEntityList]("ProviderDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *ProviderDao) WithTx(tx *gorm.DB) *ProviderDao {
	return &ProviderDao{RowDao: d.RowDao.WithTx(tx)}
}

// ModelDao 访问 models 表（v13：列 + revision CAS；平台模型清单）。
type ModelDao struct {
	*RowDao[model.ModelEntity, model.ModelEntityList]
}

// NewModelDao 构造 ModelDao。
func NewModelDao(getDB gormdao.DBGetter) *ModelDao {
	return &ModelDao{RowDao: newRowDao[model.ModelEntity, model.ModelEntityList]("ModelDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *ModelDao) WithTx(tx *gorm.DB) *ModelDao {
	return &ModelDao{RowDao: d.RowDao.WithTx(tx)}
}

// ModelCond 是 models 表的查询条件（v13 的两个引用轴：按 provider 列清单，
// 以及按 `(provider_id, model)` 判 model_exists）。
//
// `ProviderID` 总是参与筛选（空串就是查空的 provider_id，与原谓词逐字一致）；
// `Model` 为空时不筛选——"按 provider 列清单"只用前者。
type ModelCond struct {
	*gormdao.BaseCond
	ProviderID string
	Model      string
}

// BuildCondition 实现 gormdao.Cond。
func (c *ModelCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "provider_id"}, Value: c.ProviderID})
	if c.Model != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "model"}, Value: c.Model})
	}
}
