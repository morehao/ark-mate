package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— 资料库目录树（v33）的业务口径 ———
//
// 本文件是 `library_nodes` / `library_attachments` / `library_tables` 三张表的**读写面**。
// 字节不在这里（drive 节点的字节仍走 attachments/filestore）；本文件只管目录归属与节点语义。
//
// 排序/分页/关键词 LIKE 的组装在 store（dao 只留列词汇，见 guards.md v3.3）；
// 「种类↔载体一致性、成环、级联删、扩展名映射」等判决归 domain/material 与 svcmaterial。

// ErrLibraryNodeNotFound 节点不存在（供用例层 fail-closed 出口映射 40401）。
var ErrLibraryNodeNotFound = errors.New("store: library node not found")

// InsertLibraryNode 插入一条节点行。CreatedAt/UpdatedAt 零值补当前时刻。
func (s *Store) InsertLibraryNode(ctx context.Context, n model.LibraryNodeEntity) error {
	now := model.NowTSTime()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = now
	}
	return s.libraryNodes.Insert(ctx, &n)
}

// LibraryNodeByID 按 id 取一行；不存在 → (零值, false, nil)。
func (s *Store) LibraryNodeByID(ctx context.Context, id string) (model.LibraryNodeEntity, bool, error) {
	n, err := s.libraryNodes.GetByID(ctx, id)
	if err != nil {
		return model.LibraryNodeEntity{}, false, err
	}
	if n == nil {
		return model.LibraryNodeEntity{}, false, nil
	}
	return *n, true, nil
}

// ListLibraryChildren 取某空间某父节点下的一层子节点（时间序，updated_at DESC）。
//
// 空 parentID = 空间根。分页由调用方按 limit/offset 传（这里给全量再切，单机规模足够；
// 若要下推在 P2 关键词检索一起做）。
func (s *Store) ListLibraryChildren(ctx context.Context, spaceID, parentID string) ([]model.LibraryNodeEntity, error) {
	var rows model.LibraryNodeEntityList
	db := s.libraryNodes.DB(ctx).Model(&model.LibraryNodeEntity{}).
		Where("space_id = ? AND parent_id = ?", spaceID, parentID).
		Order("updated_at DESC")
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListLibraryChildrenByCategory 取某父节点下、指定分类的一层子节点（时间序，updated_at DESC）。
//
// category 空 = 不过滤（等价 ListLibraryChildren）。v34 起用于「资料/产物」分栏浏览。
func (s *Store) ListLibraryChildrenByCategory(ctx context.Context, spaceID, parentID, category string) ([]model.LibraryNodeEntity, error) {
	var rows model.LibraryNodeEntityList
	db := s.libraryNodes.DB(ctx).Model(&model.LibraryNodeEntity{}).
		Where("space_id = ? AND parent_id = ?", spaceID, parentID)
	if category != "" {
		db = db.Where("category = ?", category)
	}
	if err := db.Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// SearchLibraryNodes 关键词检索（title 或 body 子串，大小写不敏感），updated_at DESC。
//
// **这是关键词子串匹配，不是语义检索**（本期范围：只做资料库、不做知识库）。
// 返回全量命中，分页由调用方切（单机规模）。
func (s *Store) SearchLibraryNodes(ctx context.Context, spaceID, q string) ([]model.LibraryNodeEntity, error) {
	var rows model.LibraryNodeEntityList
	like := "%" + q + "%"
	err := s.libraryNodes.DB(ctx).Model(&model.LibraryNodeEntity{}).
		Where("space_id = ? AND (LOWER(title) LIKE LOWER(?) OR LOWER(body) LIKE LOWER(?))", spaceID, like, like).
		Order("updated_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateLibraryNode 更新节点可变列（title / parent_id / body）。不存在 → ErrLibraryNodeNotFound。
//
// 只接受非 nil 指针字段（指针语义，nil = 不改）；同时刷新 UpdatedAt。
func (s *Store) UpdateLibraryNode(ctx context.Context, id string, title, parentID, body *string) error {
	updates := map[string]any{"updated_at": model.NowTSTime()}
	if title != nil {
		updates["title"] = *title
	}
	if parentID != nil {
		updates["parent_id"] = *parentID
	}
	if body != nil {
		updates["body"] = *body
	}
	res := s.libraryNodes.DB(ctx).Model(&model.LibraryNodeEntity{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: id=%s", ErrLibraryNodeNotFound, id)
	}
	return nil
}

// DeleteLibraryNode 删除一条节点行（不存在视为已删，幂等）。物理字节由调用方按去重规则处理。
func (s *Store) DeleteLibraryNode(ctx context.Context, id string) error {
	return s.libraryNodes.Delete(ctx, id, "")
}

// ——— library_attachments（节点 ↔ 附件） ———

// InsertLibraryAttachment 插入一条节点↔附件关联（复合主键，重复插入由调用方避免）。
func (s *Store) InsertLibraryAttachment(ctx context.Context, a model.LibraryAttachmentEntity) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = model.NowTSTime()
	}
	return s.libraryAttachments.Insert(ctx, &a)
}

// LibraryAttachmentsByNode 取某节点的全部附件关联（created_at ASC）。
func (s *Store) LibraryAttachmentsByNode(ctx context.Context, nodeID string) ([]model.LibraryAttachmentEntity, error) {
	var rows model.LibraryAttachmentEntityList
	err := s.libraryAttachments.DB(ctx).Model(&model.LibraryAttachmentEntity{}).
		Where("node_id = ?", nodeID).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteLibraryAttachmentsByNode 删除某节点的全部附件关联（删节点时用；物理字节按去重规则由调用方处理）。
func (s *Store) DeleteLibraryAttachmentsByNode(ctx context.Context, nodeID string) error {
	return s.libraryAttachments.DB(ctx).Model(&model.LibraryAttachmentEntity{}).
		Where("node_id = ?", nodeID).Delete(&model.LibraryAttachmentEntity{}).Error
}

// ——— library_tables（table 节点的 schema + 行） ———

// UpsertLibraryTable 写入/覆盖一条表格（按 node_id 主键 upsert）。
func (s *Store) UpsertLibraryTable(ctx context.Context, t model.LibraryTableEntity) error {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = model.NowTSTime()
	}
	// 复合/非 id 主键表：先删后插（幂等覆盖），避免 gormdao 的单列 id upsert 原语不适配。
	if err := s.libraryTables.DB(ctx).Model(&model.LibraryTableEntity{}).
		Where("node_id = ?", t.NodeID).Delete(&model.LibraryTableEntity{}).Error; err != nil {
		return err
	}
	return s.libraryTables.Insert(ctx, &t)
}

// LibraryTableByNode 取某 table 节点的 schema+行；不存在 → (零值, false, nil)。
func (s *Store) LibraryTableByNode(ctx context.Context, nodeID string) (model.LibraryTableEntity, bool, error) {
	rows, err := s.libraryTables.GetListByCond(ctx, &dao.LibraryTableCond{NodeID: nodeID})
	if err != nil {
		return model.LibraryTableEntity{}, false, err
	}
	if len(rows) == 0 {
		return model.LibraryTableEntity{}, false, nil
	}
	return rows[0], true, nil
}

// DeleteLibraryTable 删除某 table 节点的表格行（幂等）。
func (s *Store) DeleteLibraryTable(ctx context.Context, nodeID string) error {
	return s.libraryTables.DB(ctx).Model(&model.LibraryTableEntity{}).
		Where("node_id = ?", nodeID).Delete(&model.LibraryTableEntity{}).Error
}

// LibraryNodeByProvenance 按 provenance_id 找已收录的节点（幂等收录用；无命中 → ok=false）。
//
// provenance_id 空串直接返回未命中（不查空串，避免把非产物节点误当命中）。
func (s *Store) LibraryNodeByProvenance(ctx context.Context, provenanceID string) (model.LibraryNodeEntity, bool, error) {
	if provenanceID == "" {
		return model.LibraryNodeEntity{}, false, nil
	}
	var rows model.LibraryNodeEntityList
	err := s.libraryNodes.DB(ctx).Model(&model.LibraryNodeEntity{}).
		Where("provenance_id = ?", provenanceID).
		Limit(1).Find(&rows).Error
	if err != nil {
		return model.LibraryNodeEntity{}, false, err
	}
	if len(rows) == 0 {
		return model.LibraryNodeEntity{}, false, nil
	}
	return rows[0], true, nil
}
