package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// WorkspaceDao 访问 workspaces 表（工作空间登记；v14）。
//
// 单表读写都在这里；**跨表不变量**（顺序重排的区间 ±1 与对账、删登记时清会话归属）
// 归 `internal/store`（见该包 `workspace.go` 的包注释）——那些语义横跨两张表，
// 落进任何一个 DAO 都会让它变成"不是 DAO 的东西"。
type WorkspaceDao struct {
	*gormdao.Dao[model.Workspace, model.WorkspaceList, string]
}

// NewWorkspaceDao 构造 WorkspaceDao（getDB 必填，见包注释）。
func NewWorkspaceDao(getDB gormdao.DBGetter) *WorkspaceDao {
	return &WorkspaceDao{Dao: newDao[model.Workspace, model.WorkspaceList, string]("WorkspaceDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *WorkspaceDao) WithTx(tx *gorm.DB) *WorkspaceDao {
	return &WorkspaceDao{Dao: d.Dao.WithTx(tx)}
}

// ListByOrder 按展示顺序列出全部登记（list_order 升序；同序时按 created_at 兜底稳定）。
func (d *WorkspaceDao) ListByOrder(ctx context.Context) (model.WorkspaceList, error) {
	var rows model.WorkspaceList
	err := d.DB(ctx).Order("list_order ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

// GetByPath 按规范化路径取一条登记（不存在 → (nil, nil)）。
//
// 它服务的是 `create` 的 **get-or-create** 语义：同一个目录重复登记必须返回既有那一行，
// 而不是报错（设计 ADR-8）——所以"查不到"是正常分支，不是错误。
func (d *WorkspaceDao) GetByPath(ctx context.Context, path string) (*model.Workspace, error) {
	var row model.Workspace
	err := d.DB(ctx).Where("path = ?", path).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetDefault 取默认工作空间（不存在 → (nil, nil)）。
//
// 「有且只有一个」由 `is_default = 1` 的部分唯一索引保证，所以这里不需要处理多行。
func (d *WorkspaceDao) GetDefault(ctx context.Context) (*model.Workspace, error) {
	var row model.Workspace
	err := d.DB(ctx).Where("is_default = ?", model.FlagValue(true)).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// MaxOrder 返回当前最大顺序；**空表返回 -1**（不是 0）。
//
// 为什么空表是 -1 而不是 0：调用方的用法是 `MaxOrder() + 1` = 新登记的 list_order，而顺序的
// 约定是 `0..n-1`（见 store.MoveWorkspace 的对账）。若空表返回 0，第一条登记就是 1——
// 库里从此带着一个从 1 开始的序列，与重排后的 0..n-1 不一致，而那种不一致没有任何界面
// 能显示出来（顺序看起来永远是对的，只是差了一个偏移）。
func (d *WorkspaceDao) MaxOrder(ctx context.Context) (int64, error) {
	var max int64
	err := d.DB(ctx).Raw(`SELECT COALESCE(MAX(list_order), -1) FROM ` + model.TableWorkspace).Scan(&max).Error
	return max, err
}

// UpdateTitle 改写展示名（updated_at 一并刷新）。
func (d *WorkspaceDao) UpdateTitle(ctx context.Context, id, title string) error {
	return d.DB(ctx).Model(&model.Workspace{}).Where("id = ?", id).
		Updates(map[string]any{"title": title, "updated_at": model.NowTS()}).Error
}

// SetOrder 直接设定某行的顺序（重排的原子动作在 store 层，见那里的对账）。
func (d *WorkspaceDao) SetOrder(ctx context.Context, id string, order int64) error {
	return d.DB(ctx).Model(&model.Workspace{}).Where("id = ?", id).
		Updates(map[string]any{"list_order": order, "updated_at": model.NowTS()}).Error
}

// Orders 返回 id → list_order 的映射（重排对账用；读的是**重排之后**的状态）。
func (d *WorkspaceDao) Orders(ctx context.Context) (map[string]int64, error) {
	var rows model.WorkspaceList
	if err := d.DB(ctx).Select("id", "list_order").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.ListOrder
	}
	return out, nil
}

// Count 登记行数。
func (d *WorkspaceDao) Count(ctx context.Context) (int64, error) {
	return d.CountByCond(ctx, &gormdao.BaseCond{})
}
