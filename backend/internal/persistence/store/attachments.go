package store

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— 附件业务索引（v32）的业务口径 ———
//
// 本文件是 `attachments` 表**唯一的写入面**：Insert（新行）、Link（挂到会话/轮次）、
// MarkOrphan（清扫）。别处不得直接写 `attachments`（守卫 G2 钉住「只有一处 Insert 调用方」）。
//
// 「附件行 ↔ filestore upload 行」的一致性由调用方（svcattachment）保证：先 `filestore.UploadAndRecord`
// 拿到 upload_id，再 `Insert`；删先删行再 `filestore.DeleteFile`。doctor 的 missing/unreadable 兜坏数据。

// InsertAttachment 插入一条附件行（唯一写入点之一）。
//
// 只做行级插入，不塞业务语义（状态机、类型探测归 svcattachment）。CreatedAt 零值补当前时刻。
func (s *Store) InsertAttachment(ctx context.Context, a model.AttachmentEntity) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = model.NowTSTime()
	}
	return s.attachments.Insert(ctx, &a)
}

// AttachmentByID 按平台附件 id 取一行；不存在 → (零值, false, nil)。
func (s *Store) AttachmentByID(ctx context.Context, id string) (model.AttachmentEntity, bool, error) {
	a, err := s.attachments.GetByID(ctx, id)
	if err != nil {
		return model.AttachmentEntity{}, false, err
	}
	if a == nil {
		return model.AttachmentEntity{}, false, nil
	}
	return *a, true, nil
}

// ListAttachmentsBySession 取某会话的全部附件（时间序）。
//
// 排序在 store（dao 只留列词汇，见 guards.md v3.3）；`created_at` 是 RFC3339Nano(UTC) 文本，
// 字符串序即时间序。读面用（会话详情、历史回放）。
func (s *Store) ListAttachmentsBySession(ctx context.Context, sessionID string) ([]model.AttachmentEntity, error) {
	var rows model.AttachmentEntityList
	err := s.attachments.DB(ctx).Model(&model.AttachmentEntity{}).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

// LinkAttachment 把一条 pending 附件挂到某会话（状态 pending → linked，补 session_id）。
//
// 归属校验（是否已属于**别的**会话、是否 pending）由调用方在受理前完成；此处只做状态迁移。
// 行不存在 → 返回 ErrAttachmentNotFound（供 fail-closed 出口）。
func (s *Store) LinkAttachment(ctx context.Context, id, sessionID string) error {
	res := s.attachments.DB(ctx).Model(&model.AttachmentEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"session_id": sessionID,
			"state":      model.AttachmentStateLinked,
		})
	if res.Error != nil {
		return fmt.Errorf("store: link attachment %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: id=%s", ErrAttachmentNotFound, id)
	}
	return nil
}

// DeleteAttachmentRow 删除一条附件行（不存在视为已删，幂等）。物理 blob 由调用方走 filestore.DeleteFile。
func (s *Store) DeleteAttachmentRow(ctx context.Context, id string) error {
	return s.attachments.Delete(ctx, id, "")
}

// ListPendingAttachmentsBefore 取 created_at 早于 before（RFC3339Nano 文本）的 pending 附件
// （孤儿清扫 ticker 用；state=pending AND created_at < before，时间序）。
//
// 时间比较用字符串序（落库格式定了定长前缀可比），before 由调用方按 `now - TTL` 算好。
func (s *Store) ListPendingAttachmentsBefore(ctx context.Context, before string) ([]model.AttachmentEntity, error) {
	var rows model.AttachmentEntityList
	err := s.attachments.DB(ctx).Model(&model.AttachmentEntity{}).
		Where("state = ? AND created_at < ?", model.AttachmentStatePending, before).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

// MarkAttachmentOrphan 把一条附件置 orphan（清扫用；幂等，只影响仍是 pending 的行）。
func (s *Store) MarkAttachmentOrphan(ctx context.Context, id string) error {
	return s.attachments.DB(ctx).Model(&model.AttachmentEntity{}).
		Where("id = ? AND state = ?", id, model.AttachmentStatePending).
		Update("state", model.AttachmentStateOrphan).Error
}

// AllAttachments 取全部附件行（doctor 只读体检用；不分页——单机产品规模）。
func (s *Store) AllAttachments(ctx context.Context) ([]model.AttachmentEntity, error) {
	var rows model.AttachmentEntityList
	err := s.attachments.DB(ctx).Model(&model.AttachmentEntity{}).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}
