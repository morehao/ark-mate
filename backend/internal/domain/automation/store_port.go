package automation

import (
	"context"
	"time"

	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
)

// AutomationStore 自动化定义与运行态的载体（v3.2 刀 7）。
//
// 六条操作全是"自动化定义这一件事"的读写：upsert / delete / get / list / 抢占一次触发 /
// 存运行态 overlay。没有一条能碰别的聚合，所以它是**聚合仓储**而不是"把库交出来"。
//
// 签名用本域的行值（`automation/row`）：行形状是适配器的语言，不是域的。`*store.Store`
// 在适配器里做行↔值映射，领域因此不认识 `adapters/outbound/sqlite/model`。
type AutomationStore interface {
	UpsertAutomationDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error)
	DeleteAutomationDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error)
	GetAutomationDefinition(ctx context.Context, id string) (*arow.Item, bool, error)
	ListAutomationDefinitions(ctx context.Context) ([]arow.Item, error)
	ClaimAutomationOccurrence(ctx context.Context, id, expectedLastFired, claimedFor string) (bool, error)
	SaveAutomationOverlay(ctx context.Context, id string, ov arow.Overlay) (bool, error)

	// ——— 投递队列（v33 / T5）———
	//
	// 这四条仍然只说"自动化这一件事"：**这条触发要送达谁、送没送到**。它们不读别的聚合，
	// 也不把库交出来——投递 worker 需要的只有"取到期的 / 记一次尝试"，没有第五个动作。

	// EnqueueAutomationDelivery 入队一条投递（幂等：同幂等键已存在 → inserted=false，返回既有行 id）。
	EnqueueAutomationDelivery(ctx context.Context, it arow.OutboxItem) (id int64, inserted bool, err error)
	// DueAutomationDeliveries 取**到期且仍 pending** 的投递（按到期时刻升序，有界窗口）。
	DueAutomationDeliveries(ctx context.Context, now time.Time, limit int) ([]arow.OutboxItem, error)
	// MarkAutomationDeliverySent 把仍 pending 的行标成已投递（CAS：挡住双投）。
	MarkAutomationDeliverySent(ctx context.Context, id int64, now time.Time) (bool, error)
	// RecordAutomationDeliveryAttempt 记录一次失败尝试（退避；giveUp = 次数用尽落 failed）。
	RecordAutomationDeliveryAttempt(ctx context.Context, id int64, attempts int64, nextAttemptAt time.Time, lastError string, giveUp bool, now time.Time) (bool, error)
	// AutomationOutboxByKey 按幂等键读一行（即时投递要用它拿到刚入队那行的 id/attempts）。
	AutomationOutboxByKey(ctx context.Context, key string) (arow.OutboxItem, bool, error)
}
