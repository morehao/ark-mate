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

// Get 读一行；不存在 → (nil, false, nil)。
func (d *RowDao[T, L]) Get(ctx context.Context, id string) (*T, bool, error) {
	var row T
	err := d.DB(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &row, true, nil
}

// Count 返回行数。
func (d *RowDao[T, L]) Count(ctx context.Context) (int64, error) {
	var n int64
	err := d.DB(ctx).Model(new(T)).Count(&n).Error
	return n, err
}

// List 列出全部行（顺序由调用方给定：`rowDaoListAll` / `rowDaoListByProviderModel`）。
//
// 排序由 SQL 给出而不是 Go 侧 `sort.Slice`：v13 的判据是「列表顺序有语义且稳定」，
// 把它交给 SQL 才能让"乱序插入 → 有序返回"这一条在数据库边界上就成立。
func (d *RowDao[T, L]) List(ctx context.Context, order string) (L, error) {
	var rows L
	err := d.DB(ctx).Order(order).Find(&rows).Error
	return rows, err
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

// 列表顺序的字面量收口：两处（SQL 与测试断言）说的是同一件事，写死在这里免得漂移。
const (
	// providerListOrder 供应商列表：按 id 升序（与 v11/v12 的 `List` 语义逐字相同）。
	providerListOrder = "id ASC"
	// modelListOrder 模型清单：**按 (provider_id, model_id) 升序**（ADR-6）。
	//
	// 为什么不是 `created_at DESC`：存量行的 created_at 是 updated_at 近似值，按它排会把
	// "最后改备注的时刻"当插入序；而 `(provider_id, model_id)` 与设置页的供应商分组一致，
	// 且对老行新行都立刻有意义。
	modelListOrder = "provider_id ASC, model_id ASC"
)

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

// List 列出全部供应商（id 升序）。
func (d *ProviderDao) List(ctx context.Context) (model.ProviderEntityList, error) {
	return d.RowDao.List(ctx, providerListOrder)
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

// ListAll 列出全部模型条目（`(provider_id, model_id)` 升序）。
//
// v12 起模型**不带 runtime 维度**，所以"列某 runtime 的清单"不再是一个查询轴：
// 同一份清单投给所有已注册底座，`ListAll` 就是同步时的那一次读取。
func (d *ModelDao) ListAll(ctx context.Context) (model.ModelEntityList, error) {
	return d.RowDao.List(ctx, modelListOrder)
}

// ListByProvider 列出挂靠在某条 provider 下的清单（删除保护的引用判据；id 升序）。
//
// **调用方必须持事务**（`s.db.Transaction` 内 `WithTx`）：v13 的引用完整性判据是
// 「检查引用 + 删行在同一事务」，脱离事务的单独调用就退回了 TOCTOU（ADR-4）。
func (d *ModelDao) ListByProvider(ctx context.Context, providerID string) (model.ModelEntityList, error) {
	var rows model.ModelEntityList
	err := d.DB(ctx).Where("provider_id = ?", providerID).Order("id ASC").Find(&rows).Error
	return rows, err
}

// GetByProviderModel 按 `(provider_id, model_id)` 取一条（`model_exists` 判定；不存在 → (nil,false,nil)）。
//
// 它对应的就是唯一索引 `idx_models_provider_model`：**判据交由库级唯一性表达**，
// 而不是"先查后写"（那中间有窗口）。
func (d *ModelDao) GetByProviderModel(ctx context.Context, providerID, modelID string) (*model.ModelEntity, bool, error) {
	var row model.ModelEntity
	err := d.DB(ctx).Where("provider_id = ? AND model_id = ?", providerID, modelID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &row, true, nil
}
