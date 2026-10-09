package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是 agent 定义事实源（M6-6）的门面：**一次写面 = 一行 + 一条审计**（与 M6-1 的能力面同构）。
//
// 与能力面的两处形态差异（不是实现偷懒，是 agent 侧的事实）：
//
//  1. **只存用户对种子的覆盖**。内置种子（dsh/opencode）永不落行——它们在代码里
//     （`manager.builtinAgents`），装载时"先铺种子再用行里的非零字段覆盖"。所以本表的行
//     可以只有 `{"id":"x","name":"..."}` 这么少，而"平台有几个 agent"由装载后的注册表回答；
//  2. **写面是 PATCH 形态**（没有 PUT）。`UpsertAgentDefinition` 的 expectedRev 由调用方在
//     "读→合并→写"窗口里持有，撞上并发写同样翻成 `definition_changed`(40901)。

// UpsertAgentDefinition 提交一次 agent 定义写入（新建或覆盖）+ 一条审计，同事务。
//
// expectedRev == 0 → 新建（revision 从 1 起；已存在则返回 false）；
// expectedRev > 0  → 覆盖，且**仅当行的 revision 仍等于它**。
// 返回 false = 该提交被并发写抢先（或行已不存在）→ 调用方翻成 `definition_changed`(40901)。
func (s *Store) UpsertAgentDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindAgent, id, actor, action, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.agents.WithTx(tx).CreateCAS(ctx, &model.AgentDefinition{
					ID: id, Body: body, Revision: 1, UpdatedAt: now,
				})
			}
			return s.agents.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteAgentDefinition 删除一条 agent 定义 + 一条审计，同事务（语义同 DeleteSkillDefinition）。
func (s *Store) DeleteAgentDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindAgent, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.agents.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetAgentDefinition 读一条 agent 定义（不存在 → (nil, false, nil)）。
func (s *Store) GetAgentDefinition(ctx context.Context, id string) (*AgentDefinition, bool, error) {
	return s.agents.Get(ctx, id)
}

// ListAgentDefinitions 列出全部 agent 定义（id 升序）。
func (s *Store) ListAgentDefinitions(ctx context.Context) (model.AgentDefinitionList, error) {
	return s.agents.List(ctx)
}

// CountAgentDefinitions 返回 agent 定义行数（导入与装载用它判断"库里有没有"）。
func (s *Store) CountAgentDefinitions(ctx context.Context) (int64, error) { return s.agents.Count(ctx) }

// ——— 「当前 Agent」的显式选择（meta KV） ———

// GetDefaultAgentID 读「当前 Agent」的显式选择（未设置 → ("", false, nil)）。
//
// 语义与 `<AgentDir>/default.json` 逐字对应：**显式选择**，不是"当前生效的 agent"——指向一个
// 已删除的 agent 时仍然原样返回（由装载层按回落链处理并告警），这样"定义回来即自动恢复"这条
// 既有行为在 DB 下同样成立。
func (s *Store) GetDefaultAgentID(ctx context.Context) (string, bool, error) {
	v, ok, err := s.meta.Get(ctx, model.MetaKeyDefaultAgent)
	if err != nil || !ok {
		return "", false, err
	}
	return v, v != "", nil
}

// SetDefaultAgentID 写「当前 Agent」的显式选择（幂等）。
func (s *Store) SetDefaultAgentID(ctx context.Context, id string) error {
	return s.meta.Set(ctx, model.MetaKeyDefaultAgent, id)
}

// ClearDefaultAgentID 清除「当前 Agent」的显式选择（回落内置默认）。
func (s *Store) ClearDefaultAgentID(ctx context.Context) error {
	return s.meta.Set(ctx, model.MetaKeyDefaultAgent, "")
}
