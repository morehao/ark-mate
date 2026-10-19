package store

import (
	"context"

	"github.com/morehao/ark-mate/internal/dao"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ——— permission_memories 表（会话级权限记忆） ———
//
// 列词汇在 `dao.PermissionMemoryCond`；"同键覆盖动哪几列"、"按授予时间排"是这里的读/写口径。

// 记忆的读序：授予时间升序，同刻按自增 id 兜底（会话内 / 全库两个读面共用）。
const (
	memoryListOrder    = "created_at ASC, id ASC"
	memoryListAllOrder = "session_id ASC, created_at ASC, id ASC"
)

// AddMemory 写入一条会话级权限记忆（同 session+title+kind 已存在时覆盖 decision；
// 同侧重复授权不刷新授予时间——与 PermissionManager.Remember 语义对齐）。
//
// created_at 的 CASE 是**有意的**：同侧重复授权不刷新授予时间，只有 allow↔reject
// 翻转才更新时间——与 PermissionManager.Remember 语义对齐，不能简化成无条件覆盖。
func (s *Store) AddMemory(ctx context.Context, m model.PermissionMemoryEntity) error {
	m.CreatedAt = nonZeroTime(m.CreatedAt)
	return s.memories.DB(ctx).Clauses(clause.OnConflict{
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
	}).Create(&m).Error
}

// ListMemories 列出某会话全部权限记忆（授予时间升序，decision 区分 allow/reject）。
func (s *Store) ListMemories(ctx context.Context, sessionID string) ([]model.PermissionMemoryEntity, error) {
	return s.memories.GetListByCond(ctx, &dao.PermissionMemoryCond{
		BaseCond:  &gormdao.BaseCond{OrderField: memoryListOrder},
		SessionID: sessionID,
	})
}

// ListAllMemories 列出全库权限记忆（sqlite 启动恢复：一次装载所有会话的 allow/reject 记忆）。
func (s *Store) ListAllMemories(ctx context.Context) ([]model.PermissionMemoryEntity, error) {
	return s.memories.GetListByCond(ctx, &dao.PermissionMemoryCond{
		BaseCond: &gormdao.BaseCond{OrderField: memoryListAllOrder},
	})
}

// DeleteMemory 撤销一条记忆（session+title+kind 精确匹配，不区分 decision）；返回是否删除。
//
// 保留手写 DELETE：三个列一起定位（`Delete` 只按 `id`），且调用方要 RowsAffected。
func (s *Store) DeleteMemory(ctx context.Context, sessionID, title, kind string) (bool, error) {
	res := s.memories.DB(ctx).Where("session_id = ? AND title = ? AND kind = ?", sessionID, title, kind).
		Delete(&model.PermissionMemoryEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteSessionMemories 一键撤销整会话全部权限记忆。
func (s *Store) DeleteSessionMemories(ctx context.Context, sessionID string) error {
	return s.memories.DB(ctx).Where("session_id = ?", sessionID).
		Delete(&model.PermissionMemoryEntity{}).Error
}
