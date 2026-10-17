package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PermissionMemoryCond 权限记忆查询条件。
type PermissionMemoryCond struct {
	*gormdao.BaseCond
	SessionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *PermissionMemoryCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(tableName+".session_id = ?", c.SessionID)
	}
}

// PermissionMemoryDao 访问 permission_memories 表（会话级权限记忆，allow/reject 双侧对称）。
type PermissionMemoryDao struct {
	*gormdao.Dao[model.PermissionMemoryEntity, model.PermissionMemoryEntityList, string]
}

// NewMemoryDao 构造 PermissionMemoryDao（getDB 必填，见包注释）。
func NewMemoryDao(getDB gormdao.DBGetter) *PermissionMemoryDao {
	return &PermissionMemoryDao{Dao: newDao[model.PermissionMemoryEntity, model.PermissionMemoryEntityList, string]("PermissionMemoryDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *PermissionMemoryDao) WithTx(tx *gorm.DB) *PermissionMemoryDao {
	return &PermissionMemoryDao{Dao: d.Dao.WithTx(tx)}
}

// Upsert 写入一条记忆（同 session+title+kind 已存在时覆盖 decision）。
//
// created_at 的 CASE 是**有意的**：同侧重复授权不刷新授予时间，只有 allow↔reject
// 翻转才更新时间——与 PermissionManager.Remember 语义对齐，不能简化成无条件覆盖。
func (d *PermissionMemoryDao) Upsert(ctx context.Context, m *model.PermissionMemoryEntity) error {
	return d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}, {Name: "title"}, {Name: "kind"}},
		DoUpdates: clause.Assignments(map[string]any{
			"decision": m.Decision,
			// **同键覆盖时也刷新这两列**（v28）：`scope` 与授权时的入参摘要描述的是"**最近一次**
			// 那条授权长什么样"。不刷新的话，同一 (title,kind) 被重新授权一次之后，库里记的仍是
			// 上一次那条调用的入参——审计时会指到一次与当前授权无关的调用上。
			"scope":              m.Scope,
			"grant_input_digest": m.GrantInputDigest,
			"created_at": gorm.Expr(
				"CASE WHEN "+model.TableNamePermissionMemory+".decision = ? THEN "+model.TableNamePermissionMemory+".created_at ELSE ? END",
				m.Decision, model.FormatTimeOrNow(m.CreatedAt)),
		}),
	}).Create(m).Error
}

// List 列出某会话全部记忆（授予时间升序，同刻按自增 id 升序）。
func (d *PermissionMemoryDao) List(ctx context.Context, sessionID string) (model.PermissionMemoryEntityList, error) {
	return d.GetListByCond(ctx, &PermissionMemoryCond{
		BaseCond:  &gormdao.BaseCond{OrderField: "created_at ASC, id ASC"},
		SessionID: sessionID,
	})
}

// ListAll 列出全库记忆（启动恢复：一次装载所有会话的 allow/reject 记忆）。
func (d *PermissionMemoryDao) ListAll(ctx context.Context) (model.PermissionMemoryEntityList, error) {
	return d.GetListByCond(ctx, &PermissionMemoryCond{
		BaseCond: &gormdao.BaseCond{OrderField: "session_id ASC, created_at ASC, id ASC"},
	})
}

// Delete 撤销一条记忆（session+title+kind 精确匹配，不区分 decision）；返回是否删除。
func (d *PermissionMemoryDao) Delete(ctx context.Context, sessionID, title, kind string) (bool, error) {
	res := d.DB(ctx).Where("session_id = ? AND title = ? AND kind = ?", sessionID, title, kind).
		Delete(&model.PermissionMemoryEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteBySession 一键撤销整会话全部权限记忆。
func (d *PermissionMemoryDao) DeleteBySession(ctx context.Context, sessionID string) error {
	return d.DB(ctx).Where("session_id = ?", sessionID).Delete(&model.PermissionMemoryEntity{}).Error
}
