package automation

// ——— T5 验收：投递 outbox（通道失败不阻断执行、补投且只投一次、退避后放弃）———
//
// 这一组用**内存假件**（`AutomationStore` / `Host` 两个端口），不起真库：域测试不许碰 SQLite
// （`TestDomainTestsStayOffSQLite`）。SQL 侧的语义（唯一索引去重、到期窗口、状态 CAS）在
// `internal/adapters/outbound/sqlite/store/automation_outbox_test.go` 里单独验。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
	"github.com/morehao/ark-mate/internal/platform/automation"
)

// fakeStore 内存版 `AutomationStore`：只把**投递队列**做成真的，其余方法足够让 RunOnce 走通。
type fakeStore struct {
	mu       sync.Mutex
	outbox   map[string]arow.OutboxItem // 幂等键 → 行（与库级唯一索引同语义）
	nextID   int64
	overlays []arow.Overlay
}

func newFakeStore() *fakeStore { return &fakeStore{outbox: map[string]arow.OutboxItem{}} }

func (f *fakeStore) UpsertAutomationDefinition(context.Context, string, string, int64, string) (bool, error) {
	return true, nil
}
func (f *fakeStore) DeleteAutomationDefinition(context.Context, string, int64, string) (bool, error) {
	return true, nil
}
func (f *fakeStore) GetAutomationDefinition(context.Context, string) (*arow.Item, bool, error) {
	return nil, false, nil
}
func (f *fakeStore) ListAutomationDefinitions(context.Context) ([]arow.Item, error) { return nil, nil }
func (f *fakeStore) ClaimAutomationOccurrence(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (f *fakeStore) SaveAutomationOverlay(_ context.Context, _ string, ov arow.Overlay) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overlays = append(f.overlays, ov)
	return true, nil
}

func (f *fakeStore) EnqueueAutomationDelivery(_ context.Context, it arow.OutboxItem) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.outbox[it.IdempotencyKey]; ok {
		return row.ID, false, nil // 唯一索引的效果：重复入队不增行
	}
	f.nextID++
	it.ID = f.nextID
	f.outbox[it.IdempotencyKey] = it
	return it.ID, true, nil
}

func (f *fakeStore) DueAutomationDeliveries(_ context.Context, now time.Time, limit int) ([]arow.OutboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []arow.OutboxItem
	for _, it := range f.outbox {
		if it.Status != arow.OutboxPending || it.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, it)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) MarkAutomationDeliverySent(_ context.Context, id int64, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, it := range f.outbox {
		if it.ID != id || it.Status != arow.OutboxPending {
			return false, nil
		}
		it.Status = arow.OutboxSent
		it.UpdatedAt = now
		f.outbox[k] = it
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) RecordAutomationDeliveryAttempt(_ context.Context, id int64, attempts int64, next time.Time, lastErr string, giveUp bool, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, it := range f.outbox {
		if it.ID != id || it.Status != arow.OutboxPending {
			return false, nil
		}
		it.Attempts = attempts
		it.NextAttemptAt = next
		it.LastError = lastErr
		it.UpdatedAt = now
		if giveUp {
			it.Status = arow.OutboxFailed
		}
		f.outbox[k] = it
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) AutomationOutboxByKey(_ context.Context, key string) (arow.OutboxItem, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.outbox[key]
	return it, ok, nil
}

// onlyRow 取唯一那行（判据用）。
func (f *fakeStore) onlyRow(t *testing.T) arow.OutboxItem {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.outbox) != 1 {
		t.Fatalf("投递队列应恰好 1 行，got %d", len(f.outbox))
	}
	for _, it := range f.outbox {
		return it
	}
	return arow.OutboxItem{}
}

// fakeHost 可控失败的宿主端口（`CreateSession` 前 N 次必失败）。
type fakeHost struct {
	mu       sync.Mutex
	failures int
	calls    []HostSessionReq
}

func (h *fakeHost) CreateSession(_ context.Context, req HostSessionReq) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, req)
	if h.failures > 0 {
		h.failures--
		return "", errors.New("底座不可用（模拟通道故障）")
	}
	return "ses_outbox_test", nil
}
func (h *fakeHost) CancelRun(context.Context, string) error                { return nil }
func (h *fakeHost) SessionRunning(string) bool                             { return false }
func (h *fakeHost) EmitRun(context.Context, string, int64, map[string]any) {}

func (h *fakeHost) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// seedDueRec 登记一条"现在就该触发"的自动化（RunOnce 会从注册表重取，所以必须真的进 recs）。
func seedDueRec(t *testing.T, pl *Plane) Rec {
	t.Helper()
	rec := Rec{Def: automation.Automation{
		ID: "nightly", Name: "夜间巡检", Enabled: true,
		Schedule: automation.Schedule{Kind: automation.KindDaily, Timezone: "UTC"},
		Target:   automation.Target{Cwd: "/tmp", Prompt: "巡检"},
	}}
	pl.mu.Lock()
	pl.recs["nightly"] = &rec
	pl.mu.Unlock()
	return rec
}

// TestDeliveryFailureDoesNotFailTheRun 通道不可用 → **执行仍成功**（不写成 Failure、不清零也不推高
// consecutive_failures）、投递留 pending；通道恢复后 worker 补投，且**只投一次**。
//
// 这条是 T5 的核心判据。反证（实跑过）：让 `Deliver` 的错误直接进 `Failure` 分支 →
// 第一段断言（Failure=false）当场红。
func TestDeliveryFailureDoesNotFailTheRun(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = old })

	st := newFakeStore()
	host := &fakeHost{failures: 1}
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetHost(host)
	rec := seedDueRec(t, pl)

	out := pl.RunOnce(context.Background(), rec, now, automation.TriggerScheduled)
	if out.Failure {
		t.Fatalf("通道失败不得把这一轮写成失败（occurrence 已认领、投递可重试）：%+v", out)
	}
	if out.SessionID != "" || out.Status != automation.RunSkipped {
		t.Fatalf("投递未送达应记 skipped 且无会话：%+v", out)
	}

	row := st.onlyRow(t)
	if row.Status != arow.OutboxPending || row.Attempts != 1 {
		t.Fatalf("应留一行 pending + 1 次尝试，got %+v", row)
	}
	if row.NextAttemptAt.Before(now.Add(outboxBaseBackoff)) {
		t.Fatalf("失败后必须退避（next_attempt_at 至少 +%s），got %v", outboxBaseBackoff, row.NextAttemptAt)
	}
	if got := host.callCount(); got != 1 {
		t.Fatalf("即时投递应只试过一次，got %d", got)
	}

	// 通道恢复 + 到点 → worker 补投一次。
	now = now.Add(outboxBaseBackoff + time.Second)
	if sent, retried, failed := pl.DrainOutbox(context.Background(), 0); sent != 1 || retried != 0 || failed != 0 {
		t.Fatalf("通道恢复后应补投成功一次，got sent=%d retried=%d failed=%d", sent, retried, failed)
	}
	if row := st.onlyRow(t); row.Status != arow.OutboxSent {
		t.Fatalf("补投成功后应落 sent，got %+v", row)
	}
	if got := host.callCount(); got != 2 {
		t.Fatalf("总投递次数应为 2（1 次失败 + 1 次补投），got %d", got)
	}

	// 再跑几轮 worker：**不许再投第二次**（这才是"补投且只投一次"）。
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if sent, retried, failed := pl.DrainOutbox(context.Background(), 0); sent+retried+failed != 0 {
			t.Fatalf("已 sent 的行不该再被处理，got sent=%d retried=%d failed=%d", sent, retried, failed)
		}
	}
	if got := host.callCount(); got != 2 {
		t.Fatalf("已送达的投递被重复投了：总次数 %d", got)
	}
}

// TestOutboxGivesUpAfterMaxAttempts 一直失败 → 次数用尽后落 failed（不再每轮重试）。
func TestOutboxGivesUpAfterMaxAttempts(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = old })

	st := newFakeStore()
	host := &fakeHost{failures: 1 << 30} // 永远失败
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetHost(host)
	rec := seedDueRec(t, pl)

	if out := pl.RunOnce(context.Background(), rec, now, automation.TriggerScheduled); out.Failure {
		t.Fatalf("即时投递失败也不该算这一轮失败：%+v", out)
	}
	// 逐轮推进时间，直到放弃。
	for i := 0; i < int(outboxMaxAttempts)+2; i++ {
		now = now.Add(outboxMaxBackoff + time.Second)
		pl.DrainOutbox(context.Background(), 0)
	}
	row := st.onlyRow(t)
	if row.Status != arow.OutboxFailed {
		t.Fatalf("次数用尽后应落 failed（默默重试与已放弃看起来一样会更难查），got %+v", row)
	}
	if row.Attempts != outboxMaxAttempts {
		t.Fatalf("放弃时应恰好尝试 %d 次，got %d", outboxMaxAttempts, row.Attempts)
	}
	if row.LastError == "" {
		t.Fatal("放弃的行必须带 last_error（否则没人知道为什么没投成）")
	}
	// 放弃之后不再尝试。
	before := host.callCount()
	now = now.Add(time.Hour)
	pl.DrainOutbox(context.Background(), 0)
	if host.callCount() != before {
		t.Fatal("已放弃的行不该再被投递")
	}
}

// TestOutboxPayloadCarriesDeliveryOrder 载荷只放"建哪个会话"的四项（**不存运行结果**）。
func TestOutboxPayloadCarriesDeliveryOrder(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = old })

	st := newFakeStore()
	host := &fakeHost{failures: 1}
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetHost(host)
	rec := seedDueRec(t, pl)
	pl.RunOnce(context.Background(), rec, now, automation.TriggerScheduled)

	row := st.onlyRow(t)
	var req HostSessionReq
	if err := json.Unmarshal([]byte(row.Payload), &req); err != nil {
		t.Fatalf("载荷应是 HostSessionReq 的直投影: %v（%s）", err, row.Payload)
	}
	if req.Prompt != "巡检" || req.Cwd != "/tmp" || req.Origin != Origin("nightly") {
		t.Fatalf("载荷字段不对：%+v", req)
	}
	if row.IdempotencyKey != OutboxKey("nightly", now.UTC().Format(time.RFC3339Nano), automation.TriggerScheduled) {
		t.Fatalf("幂等键形状不对：%q", row.IdempotencyKey)
	}
}

// TestManualTriggerSkipsOutbox 手动触发**不进**队列：用户在等回答，失败必须当场报。
func TestManualTriggerSkipsOutbox(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = old })

	st := newFakeStore()
	host := &fakeHost{failures: 1}
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetHost(host)
	rec := seedDueRec(t, pl)

	out := pl.RunOnce(context.Background(), rec, now, automation.TriggerManual)
	if !out.Failure {
		t.Fatalf("手动触发失败必须如实报 Failure：%+v", out)
	}
	if len(st.outbox) != 0 {
		t.Fatalf("手动触发不进投递队列，got %d 行", len(st.outbox))
	}
}

// TestOutboxWorkerDeliversInBackgroundAndStops 投递 worker 的**后台形态**：
// 起 goroutine → 到点自己把 pending 投出去 → ctx 结束即退出；重复 Start 不会起第二条。
//
// 为什么单独一条：其余用例都直接调 `DrainOutbox`（同步、确定），而"后台那条 goroutine"只有在
// `-race` 下才看得见问题（ticker、`outboxOnce`、与注册表锁的交叉）。它也是 `make test-race`
// 覆盖 `domain/automation` 的理由。
func TestOutboxWorkerDeliversInBackgroundAndStops(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	old := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = old })

	st := newFakeStore()
	host := &fakeHost{}
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetHost(host)
	seedDueRec(t, pl)

	// 入队一条到期的 pending（等价于"上一轮即时投递失败"）。
	if _, _, err := st.EnqueueAutomationDelivery(context.Background(), arow.OutboxItem{
		AutomationID: "nightly", Occurrence: now.Format(time.RFC3339Nano), Trigger: automation.TriggerScheduled,
		Payload:        `{"prompt":"巡检","cwd":"/tmp","origin":"automation:nightly"}`,
		IdempotencyKey: OutboxKey("nightly", now.Format(time.RFC3339Nano), automation.TriggerScheduled),
		Status:         arow.OutboxPending, NextAttemptAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("入队: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pl.StartAutomationOutboxWorker(ctx, 5*time.Millisecond)
	pl.StartAutomationOutboxWorker(ctx, 5*time.Millisecond) // 幂等：不许起第二条 worker

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok, _ := st.AutomationOutboxByKey(context.Background(), OutboxKey("nightly", now.Format(time.RFC3339Nano), automation.TriggerScheduled)); ok && row.Status == arow.OutboxSent {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if row := st.onlyRow(t); row.Status != arow.OutboxSent {
		t.Fatalf("worker 应在后台把这条投出去，got %+v", row)
	}
	if got := host.callCount(); got != 1 {
		t.Fatalf("重复 Start 不应起第二条 worker（应只投一次），got %d 次", got)
	}

	// ctx 结束 → worker 退出（不泄漏 goroutine；这里只验"取消之后不再投新的"）。
	cancel()
	time.Sleep(20 * time.Millisecond)
	before := host.callCount()
	if _, _, err := st.EnqueueAutomationDelivery(context.Background(), arow.OutboxItem{
		AutomationID: "nightly", Occurrence: now.Add(time.Minute).Format(time.RFC3339Nano), Trigger: automation.TriggerScheduled,
		Payload: `{"prompt":"巡检"}`, IdempotencyKey: "after-cancel",
		Status: arow.OutboxPending, NextAttemptAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("入队: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if host.callCount() != before {
		t.Fatal("worker 取消之后不该再投新的（ctx 结束即退出）")
	}
}

// TestStartOutboxWorkerRequiresStore 无库载体（memory 模式）不起 worker：没有队列就没有补投。
func TestStartOutboxWorkerRequiresStore(t *testing.T) {
	pl := NewPlane() // 没 SetStore
	pl.StartAutomationOutboxWorker(context.Background(), time.Millisecond)
	// 不 panic、不阻塞即为通过：判据是"它自己就不该起"。
	if sent, retried, failed := pl.DrainOutbox(context.Background(), 0); sent+retried+failed != 0 {
		t.Fatalf("没有库时 drain 应为空操作，got sent=%d retried=%d failed=%d", sent, retried, failed)
	}
}
