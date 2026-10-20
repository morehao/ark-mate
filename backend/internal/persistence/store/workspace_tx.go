package store

import (
	"context"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"gorm.io/gorm"
)

// WorkspaceTx 一段**跨聚合原子写**里可用的操作（v3.2 刀 3）。
//
// 为什么要有它：`DeleteWorkspace` 原先在一个事务里同时做两件事——判"默认工作空间不可删"
// （**领域策略**）与"把引用它的会话的归属清空"（**另一个聚合**）。策略住在仓储里，任何
// 新的调用方都只能继承它；而事务又不能拆成两次独立写（拆了就不是原子的）。
//
// 所以把事务的**开合**留在仓储（它才有库句柄），把**顺序与判定**交给领域：领域拿到这个
// 句柄，在一个事务里按自己的规则调用三个窄操作。
type WorkspaceTx struct {
	tx *gorm.DB
	s  *Store
}

// Atomically 开一个事务执行 fn；fn 返回错误即整体回滚。
func (s *Store) Atomically(ctx context.Context, fn func(*WorkspaceTx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&WorkspaceTx{tx: tx, s: s})
	})
}

// WorkspaceRow 读一行（`found=false` = 不存在，不是错误）。
func (t *WorkspaceTx) WorkspaceRow(ctx context.Context, id string) (*model.WorkspaceEntity, bool, error) {
	row, err := t.s.workspaces.WithTx(t.tx).GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
}

// ReleaseSessions 把归属该工作空间的会话清空归属（**不删会话**），返回受影响的会话数。
func (t *WorkspaceTx) ReleaseSessions(ctx context.Context, id string) (int64, error) {
	res := t.tx.Model(&model.SessionEntity{}).Where("workspace_id = ?", id).
		Updates(map[string]any{"workspace_id": ""})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// DeleteRow 删掉工作空间那一行（不做任何策略判断——判定归领域）。
func (t *WorkspaceTx) DeleteRow(ctx context.Context, id string) (bool, error) {
	res := t.tx.Delete(&model.WorkspaceEntity{}, "id = ?", id)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
