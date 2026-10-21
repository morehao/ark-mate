package store

// ——— automation_outbox（v33：自动化触发的投递队列） ———
//
// 三件事：**入队**（幂等）、**取到期的**、**记录一次尝试的结果**（状态 CAS）。
// 读面的有界窗口与退避判定都在这里（DAO 只做泛型，见守卫 `TestDaoKeepsNoBusinessReadFace`）。
//
// 为什么"记录尝试"要用 CAS（`WHERE status = pending`）：worker 单实例单 goroutine 时它看着是
// 多余的，但它的真正作用是**挡住双投**——重试路径上任何一次"读到 pending 就开始投"的竞态
// （将来多一个触发源、或进程内两条 drain 并行）都会在这里被挡住，代价只有一次 RowsAffected 判定。

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
)

// mcp/outbox 的读面默认条数（一次 drain 最多取这么多，免得一个大积压把一轮拖长）。
const automationOutboxDrainLimit = 20

// EnqueueAutomationDelivery 入队一条投递（幂等：同 `IdempotencyKey` 已存在 → inserted=false）。
//
// 返回该行的 id（已存在时是既有行的 id），调用方据此做即时尝试。**幂等实现放在库级唯一索引上**
// 而不是"先查再插"：后者在并发下会插出两行，而"两行 = 同一个 occurrence 投两次"。
func (s *Store) EnqueueAutomationDelivery(ctx context.Context, it arow.OutboxItem) (int64, bool, error) {
	now := nonZeroTime(it.CreatedAt)
	row := model.AutomationOutboxEntity{
		AutomationID:   it.AutomationID,
		Occurrence:     it.Occurrence,
		Trigger:        it.Trigger,
		Payload:        it.Payload,
		IdempotencyKey: it.IdempotencyKey,
		Status:         it.Status,
		Attempts:       it.Attempts,
		NextAttemptAt:  nonZeroTime(it.NextAttemptAt),
		LastError:      it.LastError,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idempotency_key"}},
		DoNothing: true,
	}).Create(&row)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected > 0 {
		return row.ID, true, nil
	}
	// 已存在：取回既有行的 id（调用方要拿它做即时尝试）。
	var existing model.AutomationOutboxEntity
	if err := s.db.WithContext(ctx).Where("idempotency_key = ?", it.IdempotencyKey).Take(&existing).Error; err != nil {
		return 0, false, err
	}
	return existing.ID, false, nil
}

// DueAutomationDeliveries 取**到期且仍 pending**的行（`next_attempt_at <= now`，按到期时刻升序）。
//
// limit <= 0 → 一轮最多 20 条（有界窗口：积压时一轮不该无限长，下一轮接着投）。
func (s *Store) DueAutomationDeliveries(ctx context.Context, now time.Time, limit int) ([]arow.OutboxItem, error) {
	if limit <= 0 {
		limit = automationOutboxDrainLimit
	}
	var rows model.AutomationOutboxEntityList
	err := s.db.WithContext(ctx).
		Where("status = ? AND next_attempt_at <= ?", arow.OutboxPending, model.FormatTime(nonZeroTime(now))).
		Order("next_attempt_at ASC, id ASC").Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]arow.OutboxItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, arow.OutboxItem{
			ID: r.ID, AutomationID: r.AutomationID, Occurrence: r.Occurrence, Trigger: r.Trigger,
			Payload: r.Payload, IdempotencyKey: r.IdempotencyKey, Status: r.Status,
			Attempts: r.Attempts, NextAttemptAt: r.NextAttemptAt, LastError: r.LastError,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// MarkAutomationDeliverySent 把一条**仍 pending** 的行标记为已投递；返回是否命中。
func (s *Store) MarkAutomationDeliverySent(ctx context.Context, id int64, now time.Time) (bool, error) {
	res := s.db.WithContext(ctx).Model(&model.AutomationOutboxEntity{}).
		Where("id = ? AND status = ?", id, arow.OutboxPending).
		Updates(map[string]any{
			"status":     arow.OutboxSent,
			"updated_at": model.FormatTimeOrNow(now),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// RecordAutomationDeliveryAttempt 记录一次**失败**的尝试（状态 CAS 到 pending，避免双投）。
//
// `giveUp` = 尝试次数用尽：状态落 `failed`（一条 ERROR 由调用方打——"默默放弃"与"还在重试"
// 在面板上看起来一样，是最难查的一类）。返回是否命中（未命中 = 别人先处理了这一行）。
func (s *Store) RecordAutomationDeliveryAttempt(
	ctx context.Context, id int64, attempts int64, nextAttemptAt time.Time, lastError string, giveUp bool, now time.Time,
) (bool, error) {
	status := arow.OutboxPending
	if giveUp {
		status = arow.OutboxFailed
	}
	res := s.db.WithContext(ctx).Model(&model.AutomationOutboxEntity{}).
		Where("id = ? AND status = ?", id, arow.OutboxPending).
		Updates(map[string]any{
			"status":          status,
			"attempts":        attempts,
			"next_attempt_at": model.FormatTimeOrNow(nextAttemptAt),
			"last_error":      lastError,
			"updated_at":      model.FormatTimeOrNow(now),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// AutomationOutboxByKey 按幂等键读一行（测试与排障用；不存在 → (零值, false, nil)）。
func (s *Store) AutomationOutboxByKey(ctx context.Context, key string) (arow.OutboxItem, bool, error) {
	var row model.AutomationOutboxEntity
	res := s.db.WithContext(ctx).Where("idempotency_key = ?", key).Limit(1).Find(&row)
	if res.Error != nil {
		return arow.OutboxItem{}, false, res.Error
	}
	if res.RowsAffected == 0 {
		return arow.OutboxItem{}, false, nil
	}
	return arow.OutboxItem{
		ID: row.ID, AutomationID: row.AutomationID, Occurrence: row.Occurrence, Trigger: row.Trigger,
		Payload: row.Payload, IdempotencyKey: row.IdempotencyKey, Status: row.Status,
		Attempts: row.Attempts, NextAttemptAt: row.NextAttemptAt, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, true, nil
}
