package automation

import (
	"context"

	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
)

// AutomationStore 自动化定义与运行态的载体（v3.2 刀 7）。
//
// 六条操作全是"自动化定义这一件事"的读写：upsert / delete / get / list / 抢占一次触发 /
// 存运行态 overlay。没有一条能碰别的聚合，所以它是**聚合仓储**而不是"把库交出来"。
//
// 签名用本域的行值（`automation/row`）：行形状是适配器的语言，不是域的。`*store.Store`
// 在适配器里做行↔值映射，领域因此不认识 `persistence/model`。
type AutomationStore interface {
	UpsertAutomationDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error)
	DeleteAutomationDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error)
	GetAutomationDefinition(ctx context.Context, id string) (*arow.Item, bool, error)
	ListAutomationDefinitions(ctx context.Context) ([]arow.Item, error)
	ClaimAutomationOccurrence(ctx context.Context, id, expectedLastFired, claimedFor string) (bool, error)
	SaveAutomationOverlay(ctx context.Context, id string, ov arow.Overlay) (bool, error)
}
