package store

import (
	"context"
	"sort"
	"time"

	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/dao"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// providerListOrder 供应商列表的读序（id 升序）。
const providerListOrder = "id ASC"

// 列形状归领域（`provider/row.Columns`），这里保留别名让既有调用点不改写法。
type ProviderColumns = provrow.Columns

// 本文件是 provider 档案事实源（v13 起是**资源表**）的门面：**一次写面 = 一行 + 一条审计**
// （复用 `commitDefinitionChange`）。
//
// 载体分派（sqlite = 本表 / memory = `providers.json`）在 manager 侧（`providers_carrier.go`）：
// 本包只回答"库里那一行"，不回答"这台机器上有哪些上游凭据"。
//
// **密钥现在也在库里**（v20）：`providers.key` 一列与内容列同表同行，ADR-8 的"结构上没有能放明文的列"
// 已被 v20 推翻（理由与代价见 `docs/decisions.md`「决策 v20」）。但**本包仍不碰它**：
// `SetProviderKey` / `ProviderKey` 是单独一对方法（`credentials.go`），因为密钥与内容有**两条不同的
// 写路径**——内容走 CAS + 审计（`commitDefinitionChange`），密钥只 bump revision、不落审计行。
// 于是 manager 的写面顺序是「① 校验 → ② 本行内容 CAS + 审计 → ③ `key` 列 → ④ 更新内存 → ⑤ 双写镜像」，
// ③ 失败时**不回滚** ②（回执的 `problems` 会说明）；要关掉这个窗口需要一个"内容 + 密钥同事务"的
// 门面方法，本版刻意没做（残留已记进 v20 笔记）。

// digest 这条 provider 行的规范 JSON 摘要（ADR-10）。
func providerColumnsDigest(c provrow.Columns) string {
	return rowDigest(map[string]string{
		"id": c.ID, "name": c.Name, "protocol": c.Protocol, "base_url": c.BaseURL, "note": c.Note,
	})
}

// newProviderRow 由列值构造实体行（`created_at` / `updated_at` 由调用方给）。
func newProviderRow(c ProviderColumns, rev int64, createdAt, updatedAt time.Time) *model.ProviderEntity {
	return &model.ProviderEntity{
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
	action := model.DefinitionActionUpdate
	if expectedRev == 0 {
		action = model.DefinitionActionCreate
	}
	return s.commitDefinitionChange(ctx, model.DefinitionKindProvider, cols.ID, actor, action, providerColumnsDigest(cols), expectedRev,
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
	return s.commitDefinitionChange(ctx, model.DefinitionKindProvider, cols.ID, model.DefinitionActorImport,
		model.DefinitionActionImport, providerColumnsDigest(cols), 0,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			return s.providers.WithTx(tx).CreateCAS(ctx, newProviderRow(cols, 1, now, now))
		})
}

// DeleteProviderDefinitionIfUnreferenced 删除一条 provider 档案 + 一条审计，**引用检查与删除
// 在同一事务内**（ADR-4：不依赖 FK——`models.provider_id` 上是 REFERENCES 不假，但把正确性
// 押在库的约束上就要求"所有写面都走同一条 DSN"，而本仓的只读句柄、导入路径与测试库都各自
// 开库；v1.32.22 起外键确实会咬人（ncruces 的 wasm 构建带 SQLITE_DEFAULT_FOREIGN_KEYS=1），
// 那也只是多一道兜底，不是这条同事务检查的替代）。
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
		rows, err := s.models.WithTx(tx).GetListByCond(ctx, &dao.ModelCond{
			BaseCond:   &gormdao.BaseCond{OrderField: modelByProviderOrder},
			ProviderID: id,
		})
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
		return s.definitionRevision.WithTx(tx).Append(ctx, &model.DefinitionRevisionEntity{
			Kind:         model.DefinitionKindProvider,
			DefinitionID: id,
			Action:       model.DefinitionActionDelete,
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
func (s *Store) GetProviderDefinition(ctx context.Context, id string) (*provrow.Item, bool, error) {
	row, err := s.providers.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return providerItemOf(row), true, nil
}

// ListProviderDefinitions 列出全部 provider 档案（id 升序）。
func (s *Store) ListProviderDefinitions(ctx context.Context) ([]provrow.Item, error) {
	rows, err := s.providers.GetListByCond(ctx, &gormdao.BaseCond{OrderField: providerListOrder})
	if err != nil {
		return nil, err
	}
	out := make([]provrow.Item, 0, len(rows))
	for i := range rows {
		out = append(out, *providerItemOf(&rows[i]))
	}
	return out, nil
}

// CountProviderDefinitions 返回 provider 档案行数（导入对账与 doctor 用它）。
func (s *Store) CountProviderDefinitions(ctx context.Context) (int64, error) {
	return s.providers.CountByCond(ctx, &gormdao.BaseCond{})
}

// providerItemOf 把一张 `providers` 行翻成领域值（映射只有这一处）。
func providerItemOf(r *model.ProviderEntity) *provrow.Item {
	if r == nil {
		return nil
	}
	return &provrow.Item{
		ID: r.ID, Name: r.Name, Protocol: r.Protocol, BaseURL: r.BaseURL, Note: r.Note,
		Key: r.Key, Revision: r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// providerColumnsOfImport 把导入载荷的列结构（`model.ProviderColumns`，随
// `model.DefinitionImportItem` 而来）翻成领域列值。
//
// 导入载荷是**外部正文的形状**，方向是 model → 领域，所以转换只发生在这条边界上；
// 写面其余调用点直接提交 `ProviderColumns`（= 领域列值）。
func providerColumnsOfImport(c model.ProviderColumns) provrow.Columns {
	return provrow.Columns{ID: c.ID, Name: c.Name, Protocol: c.Protocol, BaseURL: c.BaseURL, Note: c.Note}
}
