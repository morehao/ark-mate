package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是**平台模型清单**事实源（v12）的门面。
//
// 与 v11 的差别只有一处，但它是本次改造的核心：**模型不再带 runtime 维度**。
// 身份从三元组 `(runtime, provider_id, model_id)` 塌成两元组 `(provider_id, model_id)`，
// 后者由库级唯一索引真实表达（不是"提交事务里先查后写"那种约定）。
//
// 另外两个 v11 的"每 runtime"机制随之消失：
//   - `is_default` 列 → `meta.MetaKeyDefaultModel` 的**单一值**（ADR-21：用户面只有一个默认）；
//   - `model_id` 在 body 里 → `model_id` 是**列**（唯一索引要它）。
//
// 定义行仍走 `commitDefinitionChange`（行 CAS + 同事务一条审计）：这是"一次写面 = 一行 + 一条审计"
// 的既有纪律，与 skills/mcp_servers/agents 同形。

// CreateModelDefinition 新建一条模型条目 + 一条审计，同事务。
//
// 返回 false 的两种情形都是"没写进去，也没有审计"：
//   - 同 `(provider_id, model_id)` 已存在（库级唯一索引拦下；调用方翻成 `model_exists`(40901)）；
//   - 主键 `id` 已存在（并发新建抢先，同一个对外表现）。
func (s *Store) CreateModelDefinition(
	ctx context.Context, id, providerID, modelID, body, actor string,
) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionCreate, body, 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.models.WithTx(tx).CreateCAS(ctx, &model.ModelDefinition{
				ID: id, ProviderID: providerID, ModelID: modelID,
				Body: body, Revision: 1, UpdatedAt: now,
			})
		})
}

// UpsertModelDefinition 覆盖一条模型条目的正文 + 一条审计，同事务。
//
// 只碰 `body/revision/updated_at`（`DefinitionDao.UpdateCAS` 的既有语义）——`provider_id` /
// `model_id` 是身份，改它们等于换一条资源，不在覆盖语义里。
func (s *Store) UpsertModelDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindModel, id, actor, model.CapabilityActionUpdate, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.models.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteModelDefinition 删除一条模型条目 + 一条审计，同事务。
//
// 它**不**负责清默认：清不清是业务判据（v12 的语义是"允许删 + 连带清空默认"），
// 默认的落地由 manager 在同一请求里调 `ClearDefaultModelID`。
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

// ListModelDefinitions 列出全部模型条目（id 升序）。
//
// v12 起没有"列某 runtime 的清单"这个查询轴：同一份清单投给所有已注册底座。
func (s *Store) ListModelDefinitions(ctx context.Context) (model.ModelDefinitionList, error) {
	return s.models.ListAll(ctx)
}

// ListModelDefinitionsByProvider 列出挂靠在某条供应商下的条目（删除保护的引用判据）。
func (s *Store) ListModelDefinitionsByProvider(ctx context.Context, providerID string) (model.ModelDefinitionList, error) {
	return s.models.ListByProvider(ctx, providerID)
}

// FindModelDefinition 按 `(provider_id, model_id)` 取一条（`model_exists` 判定）。
func (s *Store) FindModelDefinition(ctx context.Context, providerID, modelID string) (*ModelDefinition, bool, error) {
	return s.models.GetByProviderModel(ctx, providerID, modelID)
}

// ——— 默认模型：`meta` 里的单一值（ADR-21） ———

// DefaultModelID 读默认模型（**一个**平台模型 id；未设 → ("", false, nil)）。
//
// 它不带 runtime 维度：交付时由编排层把它派生成每个底座各自的默认（`Set.DefaultModel`）。
// "读出来那个 id 还存在吗"由调用方判——模型被删掉而 meta 还留着旧值是一个**可报**的状态
// （doctor 报它、交付时忽略它），不该由读面替用户悄悄改写偏好。
func (s *Store) DefaultModelID(ctx context.Context) (string, bool, error) {
	v, ok, err := s.meta.Get(ctx, model.MetaKeyDefaultModel)
	if err != nil || !ok || v == "" {
		return "", false, err
	}
	return v, true, nil
}

// SetDefaultModelID 把默认模型设成某一条（覆盖式：meta 是 KV，天然单一值）。
func (s *Store) SetDefaultModelID(ctx context.Context, id string) error {
	return s.meta.Set(ctx, model.MetaKeyDefaultModel, id)
}

// ClearDefaultModelID 清除默认模型——**仅当它当前正是 id**（删模型时的连带清空）。
//
// 为什么带条件：删除是并发动作，"删 A"不该把用户刚设的默认 B 一起清掉。返回是否真的清了。
func (s *Store) ClearDefaultModelID(ctx context.Context, id string) (bool, error) {
	cur, ok, err := s.DefaultModelID(ctx)
	if err != nil || !ok || cur != id {
		return false, err
	}
	return true, s.meta.Set(ctx, model.MetaKeyDefaultModel, "")
}

// MigrationNotesV12 读 v12 迁移留下的待处置事实（不存在 → (nil, false, nil)）。
func (s *Store) MigrationNotesV12(ctx context.Context) (*MigrationNotes, bool, error) {
	raw, ok, err := s.meta.Get(ctx, model.MetaKeyMigrationNotes)
	if err != nil || !ok || raw == "" {
		return nil, false, err
	}
	var notes MigrationNotes
	if err := json.Unmarshal([]byte(raw), &notes); err != nil {
		return nil, false, err
	}
	return &notes, true, nil
}
