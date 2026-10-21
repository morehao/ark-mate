package store

// ——— v33 验收：投递队列的 SQL 语义（幂等键去重 / 到期窗口 / 状态 CAS / 迁移） ———
//
// 域那一侧验的是"行为"（`internal/domain/automation/outbox_test.go`，用假件）；这里验的是
// **库答应的事**：唯一索引真的挡住重复入队、到期窗口真的按 `next_attempt_at` 过滤、状态 CAS
// 真的是"只有仍 pending 的行才能被标记"（双投的最后一道闸）。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
)

// outboxRow 造一条待投递（默认 1 分钟后到期）。
func outboxRow(key string, next time.Time) arow.OutboxItem {
	return arow.OutboxItem{
		AutomationID: "nightly", Occurrence: "2026-10-21T12:00:00Z", Trigger: "scheduled",
		Payload: `{"prompt":"巡检"}`, IdempotencyKey: key, Status: arow.OutboxPending,
		NextAttemptAt: next, CreatedAt: next,
	}
}

// TestAutomationOutboxEnqueueIsIdempotent 同一个幂等键入队两次 → **只有一行**，第二次 inserted=false。
func TestAutomationOutboxEnqueueIsIdempotent(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	id1, inserted1, err := s.EnqueueAutomationDelivery(ctx, outboxRow("nightly|occ|scheduled", now))
	if err != nil || !inserted1 || id1 == 0 {
		t.Fatalf("首次入队应插入：id=%d inserted=%v err=%v", id1, inserted1, err)
	}
	id2, inserted2, err := s.EnqueueAutomationDelivery(ctx, outboxRow("nightly|occ|scheduled", now))
	if err != nil {
		t.Fatalf("重复入队: %v", err)
	}
	if inserted2 || id2 != id1 {
		t.Fatalf("重复入队不该新增行：id=%d（want %d）inserted=%v", id2, id1, inserted2)
	}
	if n := countRows(t, s, model.TableNameAutomationOutbox); n != 1 {
		t.Fatalf("幂等键唯一 → 应只有 1 行，got %d", n)
	}
}

// TestAutomationOutboxDueWindow 到期窗口按 `next_attempt_at` 过滤（退避中的行不该被投）。
func TestAutomationOutboxDueWindow(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	if _, _, err := s.EnqueueAutomationDelivery(ctx, outboxRow("due-now", now.Add(-time.Second))); err != nil {
		t.Fatalf("入队: %v", err)
	}
	if _, _, err := s.EnqueueAutomationDelivery(ctx, outboxRow("due-later", now.Add(time.Minute))); err != nil {
		t.Fatalf("入队: %v", err)
	}
	due, err := s.DueAutomationDeliveries(ctx, now, 0)
	if err != nil {
		t.Fatalf("取到期: %v", err)
	}
	if len(due) != 1 || due[0].IdempotencyKey != "due-now" {
		t.Fatalf("只该取到期的那条（退避中的不投），got %+v", due)
	}

	// 时间往后走 2 分钟 → 两条都到期。
	due, err = s.DueAutomationDeliveries(ctx, now.Add(2*time.Minute), 0)
	if err != nil || len(due) != 2 {
		t.Fatalf("到期后应取到 2 条，got %d（err=%v）", len(due), err)
	}
	// 有界窗口生效。
	due, err = s.DueAutomationDeliveries(ctx, now.Add(2*time.Minute), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("limit=1 应只取 1 条，got %d（err=%v）", len(due), err)
	}
}

// TestAutomationOutboxStatusCAS 状态 CAS：只有**仍 pending** 的行能被标记，已 sent / 已 failed 的一律拒绝。
//
// 这条挡的是"双投"：重试路径上任何一次"读到 pending 就开始投"的竞态都会在这里被挡住。
func TestAutomationOutboxStatusCAS(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	id, _, err := s.EnqueueAutomationDelivery(ctx, outboxRow("cas", now))
	if err != nil {
		t.Fatalf("入队: %v", err)
	}
	if ok, err := s.MarkAutomationDeliverySent(ctx, id, now); err != nil || !ok {
		t.Fatalf("首次标记 sent 应命中：ok=%v err=%v", ok, err)
	}
	if ok, err := s.MarkAutomationDeliverySent(ctx, id, now); err != nil || ok {
		t.Fatalf("已 sent 的行不得再被标记：ok=%v err=%v", ok, err)
	}
	// 已 sent 的行也不能再被记一次失败尝试（否则会从 sent 退回 pending）。
	if ok, err := s.RecordAutomationDeliveryAttempt(ctx, id, 2, now.Add(time.Minute), "x", false, now); err != nil || ok {
		t.Fatalf("已 sent 的行不得再记尝试：ok=%v err=%v", ok, err)
	}
	if row, ok, err := s.AutomationOutboxByKey(ctx, "cas"); err != nil || !ok || row.Status != arow.OutboxSent {
		t.Fatalf("状态应停在 sent，got %+v (ok=%v err=%v)", row, ok, err)
	}
}

// TestMigrationV33AddsOutboxTable v32 → v33：只多一张表 + 两张索引，既有行一个不动；幂等。
func TestMigrationV33AddsOutboxTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v32.db")
	database, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(database)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 32)

	seedSession(t, s, "s1")
	if _, err := s.AppendEvent(ctx, "evt_v33_probe", "s1", "session.user_message", map[string]any{"text": "迁移前"}); err != nil {
		t.Fatalf("种事件: %v", err)
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM automation_outbox`); n != -1 {
		t.Fatalf("v32 库里不该有 automation_outbox，got %d", n)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate(v33): %v", err)
	}
	for _, name := range []string{
		model.TableNameAutomationOutbox,
		"idx_automation_outbox_idem",
		"idx_automation_outbox_due",
	} {
		if n := scalarCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n == 0 {
			t.Fatalf("迁移后应有 %s", name)
		}
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM events WHERE id = 'evt_v33_probe'`); n != 1 {
		t.Fatalf("迁移不得动既有事件行，got %d", n)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
	if n := countRows(t, s, model.TableNameAutomationOutbox); n != 0 {
		t.Fatalf("重复迁移后投递队列应仍为空，got %d", n)
	}
}
