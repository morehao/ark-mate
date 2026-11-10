package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AppDao 访问 apps 表（应用定义本体，v40）。
//
// **不能复用 `DefinitionDao[T,L]`**：那张泛型 DAO 的 CAS 判据是行上的 `revision` 列，而 `apps`
// 是类型化多列表、**没有 revision 列**（v40 DDL，设计 §5.9）。本 DAO 因此只提供**机械原语**：
// 主键 CAS 插入、按 id 覆盖整行、按 id 删除、成员行整组替换——乐观锁（以 `definition_revisions.seq`
// 为当前版本）与「行 + 审计同事务」的口径住在 `store/app.go` 的 `commitDefinitionChange` 里。
//
// 与既有 DAO 纪律一致：不在这里判并发、不在这里拼审计；只留列词汇 + 泛型读方法 + 写原语。
type AppDao struct {
	*gormdao.Dao[model.AppEntity, model.AppEntityList, string]
}

// NewAppDao 构造 AppDao（getDB 必填，见包注释）。
func NewAppDao(getDB gormdao.DBGetter) *AppDao {
	return &AppDao{Dao: newDao[model.AppEntity, model.AppEntityList, string]("AppDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *AppDao) WithTx(tx *gorm.DB) *AppDao {
	return &AppDao{Dao: d.Dao.WithTx(tx)}
}

// CreateCAS 新建一条 app——主键冲突时**不报错**而是返回 false（调用方据此回 app_exists 40901，
// 与"别人先建了"是同一个对外表现）。语义同 `DefinitionDao.CreateCAS`。
func (d *AppDao) CreateCAS(ctx context.Context, row *model.AppEntity) (bool, error) {
	res := d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateFields 按 id 覆盖整行（含 updated_at）。返回是否命中一行。
//
// **不做 revision 判据**：`apps` 没有 revision 列，乐观锁在 store 事务内以 `definition_revisions.seq`
// 复核（CALLER 责任）。这里只用 id 定位——条件更新在这类"按 id 覆盖 + 外部版本仲裁"的表上
// 是正确分层：谁能写由上层的事务决定，本层只负责写。
func (d *AppDao) UpdateFields(ctx context.Context, row *model.AppEntity) (bool, error) {
	res := d.DB(ctx).Model(&model.AppEntity{}).
		Where("id = ?", row.ID).
		Select("name", "description", "avatar", "tags_json", "entities_json", "tool_domains_json",
			"slots_json", "page_json", "settings_schema_json", "api_version", "enabled", "updated_at").
		Updates(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteByID 按 id 删除一行 app，返回是否删掉。
func (d *AppDao) DeleteByID(ctx context.Context, id string) (bool, error) {
	res := d.DB(ctx).Where("id = ?", id).Delete(&model.AppEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// AppMemberDao 访问 app_members 表（app 与成员定义的多对多归属，v40）。
type AppMemberDao struct {
	*gormdao.Dao[model.AppMemberEntity, model.AppMemberEntityList, string]
}

// NewAppMemberDao 构造 AppMemberDao。
func NewAppMemberDao(getDB gormdao.DBGetter) *AppMemberDao {
	return &AppMemberDao{Dao: newDao[model.AppMemberEntity, model.AppMemberEntityList, string]("AppMemberDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *AppMemberDao) WithTx(tx *gorm.DB) *AppMemberDao {
	return &AppMemberDao{Dao: d.Dao.WithTx(tx)}
}

// ListByAppID 列出某 app 的全部成员行（按 (member_kind, member_id) 升序；读面重建成员集合用）。
func (d *AppMemberDao) ListByAppID(ctx context.Context, appID string) (model.AppMemberEntityList, error) {
	var rows model.AppMemberEntityList
	err := d.DB(ctx).Where("app_id = ?", appID).
		Order("member_kind ASC, member_id ASC").Find(&rows).Error
	return rows, err
}

// ReplaceForApp 把某 app 的成员行**整组重建**：先删旧行、再插新行（同一事务内）。
//
// 语义是「写 app 时按 members/entities/tool_domains 重建成员行」（计划 T3.1）：一条 app 的成员
// 是一等关系（`app_members`），写面把它当整体替换——这比逐行 diff 简单且不会留幽灵行。
// 开事务由调用方（store 的 commitDefinitionChange 的回调）负责。
func (d *AppMemberDao) ReplaceForApp(ctx context.Context, appID string, rows model.AppMemberEntityList) error {
	if err := d.DB(ctx).Where("app_id = ?", appID).Delete(&model.AppMemberEntity{}).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return d.DB(ctx).Create(&rows).Error
}
