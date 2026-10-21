package automation

import (
	"context"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// AutomationStore 自动化定义与运行态的载体（v3.2 刀 7）。
//
// 六条操作全是"自动化定义这一件事"的读写：upsert / delete / get / list / 抢占一次触发 /
// 存运行态 overlay。没有一条能碰别的聚合，所以它是**聚合仓储**而不是"把库交出来"。
//
// 签名与 `*store.Store` 的方法逐字相同（类型用 `model.*`，不是 store 的别名），
// 所以 `*store.Store` **结构上直接满足**，装配期不需要适配层。
type AutomationStore interface {
	UpsertAutomationDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error)
	DeleteAutomationDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error)
	GetAutomationDefinition(ctx context.Context, id string) (*model.AutomationEntity, bool, error)
	ListAutomationDefinitions(ctx context.Context) (model.AutomationEntityList, error)
	ClaimAutomationOccurrence(ctx context.Context, id, expectedLastFired, claimedFor string) (bool, error)
	SaveAutomationOverlay(ctx context.Context, id string, ov model.AutomationOverlay) (bool, error)
}
