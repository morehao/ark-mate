package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// 本文件是**应用定义**（一期 A+Slot，v40）的持久化门面。
//
// 与 `store/capability.go` 的"body + revision 窄列"定义表不同：`apps` 是类型化多列表，**没有
// revision 列**（v40 DDL，设计 §5.9）。于是乐观锁无法读本表的 revision 列——这里的口径是：
//
//	当前版本 rev(app) = `definition_revisions` 里 (kind=app, definition_id=id) 的最大 seq。
//
// 一次写 = 事务内「CAS 复核 rev(app) == expectedRev → 写 apps 行（+ 重建 app_members）→
// `commitDefinitionChange` 追加一条 kind=app 的审计」。写入成功后，新 seq 就是下一次读到的
// rev(app)——因此「写 app 时带上一版的 rev」与 expert 的「带上一版的行 revision」是同一个对外
// 语义（不中即 `definition_changed` 40901，绝不 last-write-wins）。
//
// **为什么 CAS 复核放在事务内而不是先读后写**：`definition_revisions.seq` 是唯一的并发仲裁者；
// 与写行同处一个事务，才能保证"读到的那版"与"写入的那版"之间没有别人插进来。

// listRevisionsForAppTx 取事务内某 app 的全部审计行（seq 升序）。空 → nil。
//
// 用 store 已绑定的 `definitionRevision`（不能 new 一个空 DAO 再 WithTx——那会 panic，base 未初始化）。
func (s *Store) listRevisionsForAppTx(ctx context.Context, tx *gorm.DB, id string) (model.DefinitionRevisionEntityList, error) {
	return s.definitionRevision.WithTx(tx).GetListByCond(ctx, &dao.DefinitionRevisionCond{
		BaseCond:     &gormdao.BaseCond{OrderField: "seq ASC"},
		Kind:         model.DefinitionKindApp,
		DefinitionID: id,
	})
}

// latestSeq 从升序审计行取最新 seq（无 → 0）。
func latestSeq(rows model.DefinitionRevisionEntityList) int64 {
	if len(rows) == 0 {
		return 0
	}
	return rows[len(rows)-1].Seq
}

// ListAppDefinitions 列出全部 app 定义行（id 升序，含禁用）。成员关系由调用方按 app 逐条取
// （`ListAppMembers`）——列表读面按需组装。
func (s *Store) ListAppDefinitions(ctx context.Context) (model.AppEntityList, error) {
	return s.apps.GetListByCond(ctx, &gormdao.BaseCond{OrderField: definitionListOrder})
}

// GetAppDefinition 读一条 app 行（不存在 → (nil, false, nil)）。
func (s *Store) GetAppDefinition(ctx context.Context, id string) (*model.AppEntity, bool, error) {
	row, err := s.apps.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
}

// ListAppMembers 列出某 app 的全部成员行（读面重建成员集合 / 反向查归属用）。
func (s *Store) ListAppMembers(ctx context.Context, appID string) (model.AppMemberEntityList, error) {
	return s.appMembers.ListByAppID(ctx, appID)
}

// AppRevision 返回某 app 的当前版本（= 最新审计 seq；无审计 → 0）。读面用它把 rev 回给调用方。
func (s *Store) AppRevision(ctx context.Context, id string) (int64, error) {
	rows, err := s.definitionRevision.GetListByCond(ctx, &dao.DefinitionRevisionCond{
		BaseCond:     &gormdao.BaseCond{OrderField: "seq ASC"},
		Kind:         model.DefinitionKindApp,
		DefinitionID: id,
	})
	if err != nil {
		return 0, err
	}
	return latestSeq(rows), nil
}

// appRowDigest 是 app 行那一版的内容摘要（`rowDigest` 的固定字段集口径：不含时间列）。
func appRowDigest(row *model.AppEntity) string {
	if row == nil {
		return ""
	}
	return rowDigest(map[string]string{
		"id":                   row.ID,
		"name":                 row.Name,
		"description":          row.Description,
		"tagline":              row.Tagline,
		"examples_json":        row.ExamplesJSON,
		"usage_guide":          row.UsageGuide,
		"avatar":               row.Avatar,
		"tags_json":            row.TagsJSON,
		"entities_json":        row.EntitiesJSON,
		"tool_domains_json":    row.ToolDomainsJSON,
		"slots_json":           row.SlotsJSON,
		"page_json":            row.PageJSON,
		"settings_schema_json": row.SettingsSchemaJSON,
		"api_version":          row.APIVersion,
		"enabled":              boolDigit(row.Enabled != 0),
	})
}

// boolDigit 把布尔折成 "0"/"1"（digest 输入的稳定字符串）。
func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// UpsertAppDefinition 提交一次 app 定义写入（新建或覆盖）+ 重建成员行 + 一条审计，同事务。
//
// create=true：主键冲突 → applied=false（app_exists 40901）；expectedRev 应为 0。
// create=false：仅当当前 rev(app) == expectedRev 才写（否则 applied=false → definition_changed）；
// 行不存在同样 applied=false。
//
// 成员行整组替换（`ReplaceForApp`）：写面把 members/entities/tool_domains 当整体重建。
func (s *Store) UpsertAppDefinition(
	ctx context.Context,
	id string,
	row *model.AppEntity,
	members model.AppMemberEntityList,
	expectedRev int64,
	create bool,
	actor string,
) (bool, error) {
	action := model.DefinitionActionUpdate
	if create {
		action = model.DefinitionActionCreate
	}
	digest := appRowDigest(row)
	return s.commitDefinitionChange(ctx, model.DefinitionKindApp, id, actor, action, digest, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			row.UpdatedAt = now.UnixMilli()
			if create {
				row.CreatedAt = row.UpdatedAt
				ok, err := s.apps.WithTx(tx).CreateCAS(ctx, row)
				if err != nil || !ok {
					return false, err
				}
				return true, s.appMembers.WithTx(tx).ReplaceForApp(ctx, id, members)
			}
			// update：事务内复核版本（以 definition_revisions.seq 为仲裁者）。
			revs, err := s.listRevisionsForAppTx(ctx, tx, id)
			if err != nil {
				return false, err
			}
			if latestSeq(revs) != expectedRev {
				return false, nil // CAS 没中：并发写抢先 / 行已不存在
			}
			ok, err := s.apps.WithTx(tx).UpdateFields(ctx, row)
			if err != nil || !ok {
				return false, err
			}
			return true, s.appMembers.WithTx(tx).ReplaceForApp(ctx, id, members)
		})
}

// DeleteAppDefinition 删除一条 app 定义 + 其成员行 + 一条审计，同事务（CAS 判据同 Upsert 的 update）。
//
// 行不存在 → applied=false（调用方幂等处理：不存在也算删除成功，见 svc）。
func (s *Store) DeleteAppDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.DefinitionKindApp, id, actor, model.DefinitionActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			revs, err := s.listRevisionsForAppTx(ctx, tx, id)
			if err != nil {
				return false, err
			}
			if latestSeq(revs) != expectedRev {
				return false, nil
			}
			ok, err := s.apps.WithTx(tx).DeleteByID(ctx, id)
			if err != nil || !ok {
				return false, err
			}
			// 解绑成员（删 app 后残留成员行会指向不存在的 app）。
			return true, s.appMembers.WithTx(tx).ReplaceForApp(ctx, id, nil)
		})
}

// SetAppEnabledDefinition 启停一条 app（只改 enabled 列）+ 一条审计，同事务（CAS 同 update）。
//
// 与 expert 的 SetEnabled 同构（禁用只影响新选择，不影响已建会话）：这里只翻 enabled，
// 不重建成员行（成员关系与启停正交）。
func (s *Store) SetAppEnabledDefinition(ctx context.Context, id string, enabled bool, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.DefinitionKindApp, id, actor, model.DefinitionActionUpdate, "", expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			revs, err := s.listRevisionsForAppTx(ctx, tx, id)
			if err != nil {
				return false, err
			}
			if latestSeq(revs) != expectedRev {
				return false, nil
			}
			en := 0
			if enabled {
				en = 1
			}
			res := s.apps.WithTx(tx).DB(ctx).Model(&model.AppEntity{}).
				Where("id = ?", id).
				Updates(map[string]any{"enabled": en, "updated_at": now.UnixMilli()})
			if res.Error != nil {
				return false, res.Error
			}
			return res.RowsAffected > 0, nil
		})
}
