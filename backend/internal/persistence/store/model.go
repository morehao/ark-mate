package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// 模型清单的读序：全局按 `(provider_id, model)`（ADR-6）；按 provider 过滤时按 id。
const (
	modelListOrder       = "provider_id ASC, model ASC"
	modelByProviderOrder = "id ASC"
)

// 本文件是**平台模型清单**事实源（v13 起是**资源表**）的门面。
//
// 与 v12 的差别只有存储形态（列化 + 删 body），语义逐字不变：
//   - **没有 runtime 维度**：一份清单投给所有已注册底座（ADR-1/B3）；
//   - **默认模型是单一值**（`meta.MetaKeyDefaultModel`，ADR-21）：它是**用户的偏好**，
//     不是模型行的属性；
//   - 列表顺序 = `(provider_id, model)` 升序（ADR-6，修 P1 的"UUID 字典序"）。
//
// 定义行仍走 `commitDefinitionChange`（行 CAS + 同事务一条审计）：这是
// "一次写面 = 一行 + 一条审计"的既有纪律，与 skills/mcp_servers/agents 同形。
//
// 清单需要 sqlite 持久层：memory 模式没有 `models` 表（写面在 manager 侧报 40501）。

// model.ModelColumns 是模型行的**全部列**（含身份列）：新建时用。
// digest 这条模型行的规范 JSON 摘要（ADR-10）。
//
// 身份两列**进摘要**（它们是这一版内容的一部分）；`revision` / 时间列不进（记账不是内容）。
//
// 容量（v25）也进摘要：它是**内容**，改了它交付字节就变。nil 与 0 在这里必须**可区分**——nil 记
// 空串（"未声明"），有值记十进制（`0` 不是合法容量，落库前已被写面折成 nil）。
func modelRowDigest(cols model.ModelColumns) string {
	return rowDigest(map[string]string{
		"id": cols.ID, "provider_id": cols.ProviderID, "model": cols.Model,
		"name": cols.Name, "note": cols.Note, "input_modalities": cols.InputModalities,
		"max_input_tokens":  capacityDigest(cols.MaxInputTokens),
		"max_output_tokens": capacityDigest(cols.MaxOutputTokens),
	})
}

// capacityDigest 容量的摘要形态（nil = 空串；有值 = 十进制）。
//
// 为什么不直接 `strconv.FormatInt(*v)` 混着来：nil 解引用会 panic，而 `fmt.Sprint(nil)` 是
// `<nil>`——那会把"未声明"记成一个看起来像值的串。空串是唯一不会与任何合法取值撞车的形态。
func capacityDigest(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

// CreateModelDefinition 新建一条模型条目 + 一条审计，同事务。
//
// 返回 false 的两种情形都是"没写进去，也没有审计"：
//   - 同 `(provider_id, model)` 已存在（库级唯一索引拦下；调用方翻成 `model_exists`(40901)）；
//   - 主键 `id` 已存在（并发新建抢先，同一个对外表现）。
func (s *Store) CreateModelDefinition(ctx context.Context, cols model.ModelColumns, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.DefinitionKindModel, cols.ID, actor,
		model.DefinitionActionCreate, modelRowDigest(cols), 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.models.WithTx(tx).CreateCAS(ctx, &model.ModelEntity{
				ID: cols.ID, ProviderID: cols.ProviderID, Model: cols.Model,
				Name: cols.Name, Note: cols.Note, InputModalities: cols.InputModalities,
				MaxInputTokens: cols.MaxInputTokens, MaxOutputTokens: cols.MaxOutputTokens,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
		})
}

// UpdateModelDefinition 覆盖一条模型条目的**内容列** + 一条审计，同事务。
//
// 只碰 `name` / `note` / `input_modalities` / `max_input_tokens` / `max_output_tokens` /
// `revision` / `updated_at`——`provider_id` / `model` 是身份，改它们等于换一条资源，不在覆盖语义里。
func (s *Store) UpdateModelDefinition(ctx context.Context, id string, content model.ModelContent, expectedRev int64, actor string) (bool, error) {
	digest := rowDigest(map[string]string{
		"id": id, "name": content.Name, "note": content.Note, "input_modalities": content.InputModalities,
		"max_input_tokens":  capacityDigest(content.MaxInputTokens),
		"max_output_tokens": capacityDigest(content.MaxOutputTokens),
	})
	return s.commitDefinitionChange(ctx, model.DefinitionKindModel, id, actor,
		model.DefinitionActionUpdate, digest, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.models.WithTx(tx).UpdateColumnsCAS(ctx, id, map[string]any{
				"name": content.Name, "note": content.Note, "input_modalities": content.InputModalities,
				"max_input_tokens": content.MaxInputTokens, "max_output_tokens": content.MaxOutputTokens,
			}, expectedRev, now)
		})
}

// DeleteModelDefinitionAndClearDefault 删除一条模型条目 + 一条审计，**并在同一事务内**
// 条件清空 `meta.default_model`。
//
// 为什么两件事必须同事务（v12 是两次独立调用）：中间那一小段里"默认指向一条已删除的行"
// 是一个**悬挂态**——交付时被忽略、读面显示"默认模型：无"，而 meta 里还留着旧 id
// （B6，修 bug 性质）。条件清空（仅当默认**正是这一行**）不变：删除是并发动作，
// "删 A"不该把用户刚设的默认 B 一起清掉。
//
// 返回 (是否删掉, 是否清了默认)。
func (s *Store) DeleteModelDefinitionAndClearDefault(
	ctx context.Context, id string, expectedRev int64, actor string,
) (bool, bool, error) {
	now := time.Now().UTC()
	deleted, cleared := false, false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok, err := s.models.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		if err != nil {
			return err
		}
		if !ok {
			return nil // 行不在 / CAS 未中：不写审计、也不动默认
		}
		deleted = true
		cur, has, err := s.meta.WithTx(tx).Get(ctx, model.MetaKeyDefaultModel)
		if err != nil {
			return err
		}
		if has && cur == id {
			if err := s.meta.WithTx(tx).Set(ctx, model.MetaKeyDefaultModel, ""); err != nil {
				return err
			}
			cleared = true
		}
		return s.definitionRevision.WithTx(tx).Append(ctx, &model.DefinitionRevisionEntity{
			Kind:         model.DefinitionKindModel,
			DefinitionID: id,
			Action:       model.DefinitionActionDelete,
			Actor:        actor,
			At:           now,
			Digest:       "",
		})
	})
	if err != nil {
		return false, false, err
	}
	return deleted, cleared, nil
}

// GetModelDefinition 读一条模型条目（不存在 → (nil, false, nil)）。
func (s *Store) GetModelDefinition(ctx context.Context, id string) (*model.ModelEntity, bool, error) {
	row, err := s.models.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
}

// ListModelDefinitions 列出全部模型条目（**`(provider_id, model)` 升序**，ADR-6）。
//
// v12 起没有"列某 runtime 的清单"这个查询轴：同一份清单投给所有已注册底座。
func (s *Store) ListModelDefinitions(ctx context.Context) (model.ModelEntityList, error) {
	return s.models.GetListByCond(ctx, &gormdao.BaseCond{OrderField: modelListOrder})
}

// ListModelDefinitionsByProvider 列出挂靠在某条供应商下的条目（**只读**：删除保护的判据
// 已移进 `DeleteProviderDefinitionIfUnreferenced` 的同一事务，这里只服务文案与 doctor）。
func (s *Store) ListModelDefinitionsByProvider(ctx context.Context, providerID string) (model.ModelEntityList, error) {
	return s.models.GetListByCond(ctx, &dao.ModelCond{
		BaseCond:   &gormdao.BaseCond{OrderField: modelByProviderOrder},
		ProviderID: providerID,
	})
}

// FindModelDefinition 按 `(provider_id, model)` 取一条（`model_exists` 判定）。
//
// 空标识直接判"没有"：`ModelCond.Model` 是可选的（`ListModelDefinitionsByProvider` 只给
// provider），不早退会退化成"取该 provider 下任意一条"——那是静默读错行。
func (s *Store) FindModelDefinition(ctx context.Context, providerID, upstreamModel string) (*model.ModelEntity, bool, error) {
	if providerID == "" || upstreamModel == "" {
		return nil, false, nil
	}
	row, err := s.models.GetByCond(ctx, &dao.ModelCond{ProviderID: providerID, Model: upstreamModel})
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
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

// ClearDefaultModelID 清除默认模型——**仅当它当前正是 id**（PATCH 里 `is_default:false` 的落地）。
//
// 删模型那条路径**不用**它：那条要求"删行 + 清默认"同事务，走
// `DeleteModelDefinitionAndClearDefault`。这里保留给"只改偏好、不动行"的写面。
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

// MigrationNotesV13 读 v13 迁移留下的待处置事实（不存在 → (nil, false, nil)）。
//
// 与 `MigrationNotesV12` 分键读取是刻意的：v13 覆盖 v12 的键等于把 v12 那些尚未处置的
// 事实静默抹掉（`MetaKeyMigrationNotesV13` 的注释）。
func (s *Store) MigrationNotesV13(ctx context.Context) (*MigrationNotesV13, bool, error) {
	raw, ok, err := s.meta.Get(ctx, model.MetaKeyMigrationNotesV13)
	if err != nil || !ok || raw == "" {
		return nil, false, err
	}
	var notes MigrationNotesV13
	if err := json.Unmarshal([]byte(raw), &notes); err != nil {
		return nil, false, err
	}
	return &notes, true, nil
}
