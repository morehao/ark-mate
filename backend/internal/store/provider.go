package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是 provider 档案事实源（v11，设置 · 模型）的门面：**一次写面 = 一行 + 一条审计**
// （与 skills/mcp_servers/agents/automations 同构，复用 `commitDefinitionChange`）。
//
// 载体分派（sqlite = 本表 / memory = `providers.json`）在 manager 侧（`providers_carrier.go`）：
// 本包只回答"库里那一行"，不回答"这台机器上有哪些上游凭据"。
//
// **不碰 vault**：密钥不是定义。`Body` 里只允许 `key_ref`，密文归 `vault`（KindProviderKey）。
// vault 与 SQLite 无法同事务，所以写面顺序由 manager 固定为
// 「① 校验 → ② vault 写入/删除 → ③ 本行 CAS + 审计 → ④ 更新内存」，③ 失败时回滚 ②。

// UpsertProviderDefinition 提交一次 provider 档案写入（新建或覆盖）+ 一条审计，同事务。
//
// expectedRev == 0 → 新建（revision 从 1 起；已存在则返回 false）；
// expectedRev > 0  → 覆盖，且**仅当行的 revision 仍等于它**。
// 返回 false = 该提交被并发写抢先（或行已不存在）→ 调用方翻成 `definition_changed`(40901)，
// 而与"publish 语义收紧后的 PATCH 打到不存在的档案"区分开（后者在 manager 侧先查行 → 40401）。
func (s *Store) UpsertProviderDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindProvider, id, actor, action, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.providers.WithTx(tx).CreateCAS(ctx, &model.ProviderDefinition{
					ID: id, Body: body, Revision: 1, UpdatedAt: now,
				})
			}
			return s.providers.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// ImportProviderDefinition 是装配期**一次性幂等导入**专用的新建路径：
// 仅在行不存在时插入（已存在 → false，不动行也不追审计），审计 action=import。
//
// 与 `UpsertProviderDefinition(…, 0, …)` 的差别只有一个字段（action 是 `import` 而不是
// `create`）——但那正是审计要能回答的问题："这一行是用户建的，还是升级时从文件搬过来的"。
func (s *Store) ImportProviderDefinition(ctx context.Context, id, body string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindProvider, id, model.CapabilityActorImport,
		model.CapabilityActionImport, body, 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.providers.WithTx(tx).CreateCAS(ctx, &model.ProviderDefinition{
				ID: id, Body: body, Revision: 1, UpdatedAt: now,
			})
		})
}

// DeleteProviderDefinition 删除一条 provider 档案 + 一条审计，同事务（语义同 DeleteSkillDefinition）。
func (s *Store) DeleteProviderDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindProvider, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.providers.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetProviderDefinition 读一条 provider 档案（不存在 → (nil, false, nil)）。
func (s *Store) GetProviderDefinition(ctx context.Context, id string) (*ProviderDefinition, bool, error) {
	return s.providers.Get(ctx, id)
}

// ListProviderDefinitions 列出全部 provider 档案（id 升序）。
func (s *Store) ListProviderDefinitions(ctx context.Context) (model.ProviderDefinitionList, error) {
	return s.providers.List(ctx)
}

// CountProviderDefinitions 返回 provider 档案行数（导入对账与 doctor 用它）。
func (s *Store) CountProviderDefinitions(ctx context.Context) (int64, error) {
	return s.providers.Count(ctx)
}
