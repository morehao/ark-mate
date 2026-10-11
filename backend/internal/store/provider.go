package store

import (
	"context"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是 provider 档案事实源（v13 起是**资源表**）的门面：**一次写面 = 一行 + 一条审计**
// （复用 `commitDefinitionChange`）。
//
// 载体分派（sqlite = 本表 / memory = `providers.json`）在 manager 侧（`providers_carrier.go`）：
// 本包只回答"库里那一行"，不回答"这台机器上有哪些上游凭据"。
//
// **不碰 vault**：密钥不是定义，而 v13 的两张表**结构上没有能放明文的列**（ADR-8）。
// vault 与 SQLite 无法同事务，所以写面顺序由 manager 固定为
// 「① 校验 → ② vault 写入/删除 → ③ 本行 CAS + 审计 → ④ 更新内存」，③ 失败时回滚 ②。

// ProviderColumns 是 provider 行的**内容列**（身份列是 `ID`，记账列由 CAS 语义决定）。
//
// 它就是 `providerBodyJSON` 的替代物：列化之后不再有"未知键原样带走"这回事——键集合是封闭的，
// 于是"写了、存了、什么都不发生"的字段结构上进不来（设计 P7）。
type ProviderColumns struct {
	ID       string
	Name     string
	Protocol string
	BaseURL  string
	Note     string
}

// digest 这条 provider 行的规范 JSON 摘要（ADR-10）。
func (c ProviderColumns) digest() string {
	return rowDigest(map[string]string{
		"id": c.ID, "name": c.Name, "protocol": c.Protocol, "base_url": c.BaseURL, "note": c.Note,
	})
}

// newProviderRow 由列值构造实体行（`created_at` / `updated_at` 由调用方给）。
func newProviderRow(c ProviderColumns, rev int64, createdAt, updatedAt time.Time) *model.ProviderDefinition {
	return &model.ProviderDefinition{
		ID: c.ID, Name: c.Name, Protocol: c.Protocol, BaseURL: c.BaseURL, Note: c.Note,
		Revision: rev, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// providerContentUpdates 覆盖时只改内容列（身份列与 created_at 不参与）。
func providerContentUpdates(c ProviderColumns) map[string]any {
	return map[string]any{
		"name": c.Name, "protocol": c.Protocol, "base_url": c.BaseURL, "note": c.Note,
	}
}

// UpsertProviderDefinition 提交一次 provider 档案写入（新建或覆盖）+ 一条审计，同事务。
//
// expectedRev == 0 → 新建（revision 从 1 起；已存在则返回 false）；
// expectedRev > 0  → 覆盖，且**仅当行的 revision 仍等于它**。
// 返回 false = 该提交被并发写抢先（或行已不存在）→ 调用方翻成 `definition_changed`(40901)，
// 而与"publish 语义收紧后的 PATCH 打到不存在的档案"区分开（后者在 manager 侧先查行 → 40401）。
func (s *Store) UpsertProviderDefinition(ctx context.Context, cols ProviderColumns, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindProvider, cols.ID, actor, action, cols.digest(), expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.providers.WithTx(tx).CreateCAS(ctx, newProviderRow(cols, 1, now, now))
			}
			return s.providers.WithTx(tx).UpdateColumnsCAS(ctx, cols.ID, providerContentUpdates(cols), expectedRev, now)
		})
}

// ImportProviderDefinition 是装配期**一次性幂等导入**专用的新建路径：
// 仅在行不存在时插入（已存在 → false，不动行也不追审计），审计 action=import。
//
// 与 `UpsertProviderDefinition(…, 0, …)` 的差别只有一个字段（action 是 `import` 而不是
// `create`）——但那正是审计要能回答的问题："这一行是用户建的，还是升级时从文件搬过来的"。
func (s *Store) ImportProviderDefinition(ctx context.Context, cols ProviderColumns) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindProvider, cols.ID, model.CapabilityActorImport,
		model.CapabilityActionImport, cols.digest(), 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.providers.WithTx(tx).CreateCAS(ctx, newProviderRow(cols, 1, now, now))
		})
}

// DeleteProviderDefinitionIfUnreferenced 删除一条 provider 档案 + 一条审计，**引用检查与删除
// 在同一事务内**（ADR-4：不依赖 FK，因为本仓 SQLite 的 `foreign_keys=0`，所有 REFERENCES
// 都只是装饰）。
//
// 返回：
//   - deleted=false + refModelIDs 为空：行不存在 / revision CAS 未中（调用方翻 40901 或幂等 false）；
//   - deleted=false + refModelIDs 非空：**有模型挂靠它**，什么都没删、也没写审计；
//   - deleted=true：行与审计都已提交。
//
// 为什么必须同事务：旧形态是「先查引用（另一次 SELECT）→ 再删行」，中间有窗口——并发
// 「删供应商」+「给它加模型」会留下孤儿 model 行（设计 P3）。审计行也在同一事务里，
// 于是"行没了但审计没写"或反过来的半截状态不可能出现。
func (s *Store) DeleteProviderDefinitionIfUnreferenced(
	ctx context.Context, id string, expectedRev int64, actor string,
) (bool, []string, error) {
	now := time.Now().UTC()
	deleted := false
	var refs []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := s.models.WithTx(tx).ListByProvider(ctx, id)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			refs = make([]string, 0, len(rows))
			for i := range rows {
				refs = append(refs, rows[i].ID)
			}
			sort.Strings(refs)
			return nil
		}
		ok, err := s.providers.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		if err != nil || !ok {
			return err
		}
		deleted = true
		return s.capabilityRevision.WithTx(tx).Append(ctx, &model.CapabilityRevision{
			Kind:         model.CapabilityKindProvider,
			CapabilityID: id,
			Action:       model.CapabilityActionDelete,
			Actor:        actor,
			At:           now,
			Digest:       "", // 删除没有"提交后的那一版"，沿用 bodyDigest("") 的口径
		})
	})
	if err != nil {
		return false, nil, err
	}
	return deleted, refs, nil
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
