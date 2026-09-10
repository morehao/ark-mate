package dao

import (
	"context"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MemoryCond 权限记忆查询条件。
type MemoryCond struct {
	*gormdao.BaseCond
	ConversationID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *MemoryCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.ConversationID != "" {
		db.Where(tableName+".conversation_id = ?", c.ConversationID)
	}
}

// MemoryDao 访问 permission_memories 表（会话级权限记忆，allow/reject 双侧对称）。
type MemoryDao struct {
	*gormdao.Dao[model.Memory, model.MemoryList, int64]
}

// NewMemoryDao 构造 MemoryDao（getDB 必填，见包注释）。
func NewMemoryDao(getDB gormdao.DBGetter) *MemoryDao {
	return &MemoryDao{Dao: newDao[model.Memory, model.MemoryList, int64]("MemoryDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *MemoryDao) WithTx(tx *gorm.DB) *MemoryDao {
	return &MemoryDao{Dao: d.Dao.WithTx(tx)}
}

// Upsert 写入一条记忆（同 conversation+title+kind 已存在时覆盖 decision）。
//
// created_at 的 CASE 是**有意的**：同侧重复授权不刷新授予时间，只有 allow↔reject
// 翻转才更新时间——与 PermissionManager.Remember 语义对齐，不能简化成无条件覆盖。
func (d *MemoryDao) Upsert(ctx context.Context, m *model.Memory) error {
	return d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "conversation_id"}, {Name: "title"}, {Name: "kind"}},
		DoUpdates: clause.Assignments(map[string]any{
			"decision": m.Decision,
			"created_at": gorm.Expr(
				"CASE WHEN "+model.TableMemory+".decision = ? THEN "+model.TableMemory+".created_at ELSE ? END",
				m.Decision, model.FormatTimeOrNow(m.CreatedAt)),
		}),
	}).Create(m).Error
}

// List 列出某会话全部记忆（授予时间升序，同刻按自增 id 升序）。
func (d *MemoryDao) List(ctx context.Context, conversationID string) (model.MemoryList, error) {
	return d.GetListByCond(ctx, &MemoryCond{
		BaseCond:       &gormdao.BaseCond{OrderField: "created_at ASC, id ASC"},
		ConversationID: conversationID,
	})
}

// ListAll 列出全库记忆（启动恢复：一次装载所有会话的 allow/reject 记忆）。
func (d *MemoryDao) ListAll(ctx context.Context) (model.MemoryList, error) {
	return d.GetListByCond(ctx, &MemoryCond{
		BaseCond: &gormdao.BaseCond{OrderField: "conversation_id ASC, created_at ASC, id ASC"},
	})
}

// Delete 撤销一条记忆（conversation+title+kind 精确匹配，不区分 decision）；返回是否删除。
func (d *MemoryDao) Delete(ctx context.Context, conversationID, title, kind string) (bool, error) {
	res := d.DB(ctx).Where("conversation_id = ? AND title = ? AND kind = ?", conversationID, title, kind).
		Delete(&model.Memory{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteByConversation 一键撤销整会话全部权限记忆。
func (d *MemoryDao) DeleteByConversation(ctx context.Context, conversationID string) error {
	return d.DB(ctx).Where("conversation_id = ?", conversationID).Delete(&model.Memory{}).Error
}
