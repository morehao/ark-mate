package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是**平台模型清单**事实源（v11，设置 · 模型）的门面。
//
// 它与 providers 面共用同一条提交路径（`commitDefinitionChange`：行 CAS + 同事务一条审计），
// 但多两条本表专有的不变量：
//
//  1. **`(runtime, provider_id, model_id)` 唯一**。前两列是列、`model_id` 在 `body` 里，
//     所以库级唯一索引表达不了——判据是**提交事务内**的一次存在性查询（不是"先查后写"，
//     那中间有窗口）。
//  2. **每个 runtime 至多一条 `is_default`**。判据是同一事务里的两条语句
//     （先 `ClearDefaults(runtime)` 再 `SetDefaultCAS`），单表两句在 `commitDefinitionChange`
//     的事务里天然原子。
//
// 两条都刻意**不由调用方维持**：它们是数据不变量，落在写面的唯一入口上才不会漏。

// CreateModelDefinition 新建一条模型条目 + 一条审计，同事务。
//
// 返回 false 的两种情形都是"没写进去，也没有审计"：
//   - 同 `(runtime, provider_id, model_id)` 已存在（调用方翻成 `model_exists`(40901)）；
//   - 主键 `id` 已存在（并发新建抢先，同一个对外表现）。
func (s *Store) CreateModelDefinition(
	ctx context.Context, id, runtimeID, providerID, modelID, body, actor string,
) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionCreate, body, 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			txDao := s.models.WithTx(tx)
			rows, err := txDao.ListByProvider(ctx, providerID)
			if err != nil {
				return false, err
			}
			for i := range rows {
				if rows[i].Runtime == runtimeID && ModelIDOfBody(rows[i].Body) == modelID {
					return false, nil
				}
			}
			return txDao.CreateCAS(ctx, &model.ModelDefinition{
				ID: id, Runtime: runtimeID, ProviderID: providerID,
				Body: body, Revision: 1, UpdatedAt: now,
			})
		})
}

// UpsertModelDefinition 覆盖一条模型条目的正文 + 一条审计，同事务。
//
// `DefinitionDao.UpdateCAS` 只碰 `body/revision/updated_at`，**不动 `is_default`**——
// 于是"改别名/隐藏/置顶"不会顺手把默认位清掉（这正是列与正文分开的一处收益）。
// 置默认有它自己的入口（SetDefaultModel）。
func (s *Store) UpsertModelDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionUpdate, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.models.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteModelDefinition 删除一条模型条目 + 一条审计，同事务。
//
// **不做"删掉默认就顺手置空"的兜底**：删除默认模型必须在写面被拒（`model_is_default`(40501)），
// 否则「平台的默认模型」会在一个用户看不见的时刻消失（设计「接口设计」的 DELETE 行）。
// 判据由 manager 在读→删窗口里给出（它持有行的 revision）。
func (s *Store) DeleteModelDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.models.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetModelDefinition 读一条模型条目（不存在 → (nil, false, nil)）。
func (s *Store) GetModelDefinition(ctx context.Context, id string) (*ModelDefinition, bool, error) {
	return s.models.Get(ctx, id)
}

// ListModelDefinitions 列出模型清单（runtimeID 为空 = 全部；id 升序）。
func (s *Store) ListModelDefinitions(ctx context.Context, runtimeID string) (model.ModelDefinitionList, error) {
	return s.models.ListByRuntime(ctx, runtimeID)
}

// ListModelDefinitionsByProvider 列出挂靠在某条 provider 下的条目（删除保护的引用判据）。
func (s *Store) ListModelDefinitionsByProvider(ctx context.Context, providerID string) (model.ModelDefinitionList, error) {
	return s.models.ListByProvider(ctx, providerID)
}

// ListDefaultModelDefinitions 列出全部 runtime 的默认模型（跨 runtime，runtime 升序）。
func (s *Store) ListDefaultModelDefinitions(ctx context.Context) (model.ModelDefinitionList, error) {
	return s.models.ListDefaults(ctx)
}

// DefaultModelDefinition 读某 runtime 的默认模型（不存在 → (nil, false, nil)）。
//
// 它是 T2 快路径（会话创建时下发默认模型）与 T5 慢路径（物化进配置文件）**共用的取值面**：
// 两条路读同一个 `is_default`，所以"底座自己认的默认"与"平台下发的默认"逐字同源。
func (s *Store) DefaultModelDefinition(ctx context.Context, runtimeID string) (*ModelDefinition, bool, error) {
	return s.models.GetDefault(ctx, runtimeID)
}

// SetDefaultModel 把一条模型置为该 runtime 的默认（清旧默认 + 置新默认 + 一条审计，同事务）。
//
// 返回 false = 行不存在，或 CAS 未中（有人在这中间改过这一行）→ 调用方翻成
// `model_not_found`(40401) / `definition_changed`(40901)。
// 已经是默认 → 直接返回 true（幂等，不写无意义的一版）。
//
// 审计的 digest 是**正文**的摘要，而 `is_default` 在列上——所以"置默认"这一版审计里
// digest 与上一版相同。这是刻意的取舍：拿 body 摘要当审计指纹的既有纪律不破，
// 而"谁在什么时候把默认换成了哪个"由审计的 `at`/`actor` 与 `capability_id` 回答。
func (s *Store) SetDefaultModel(ctx context.Context, id, actor string) (bool, error) {
	row, found, err := s.models.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if row.IsDefault {
		return true, nil
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionUpdate, row.Body, row.Revision,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			txDao := s.models.WithTx(tx)
			if err := txDao.ClearDefaults(ctx, row.Runtime); err != nil {
				return false, err
			}
			return txDao.SetDefaultCAS(ctx, id, row.Revision, now)
		})
}

// ModelIDOfBody 从一条模型条目的正文里取 `model_id`。
//
// 唯一性判据 `(runtime, provider_id, model_id)` 的第三项在 body 里（见 ModelDefinition 注释），
// 所以这里必须解析一个 key——它不是一个"业务字段的便利访问器"，而是**约束的一部分**。
// 解析失败返回空串：空串与任何真实 `model_id` 都不相等，于是坏正文不会被误判成"重复"，
// 而是走到 create 分支（坏正文本身由写面校验拦下）。
func ModelIDOfBody(body string) string {
	var probe struct {
		ModelID string `json:"model_id"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return ""
	}
	return probe.ModelID
}
