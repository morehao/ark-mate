package manager

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
)

// ——— P2b：自动化域的过渡期转发与别名 ———
//
// 定义装载/读写（两种载体）、排期与到期判定、触发一次的认领与写回、运行史与实时事件、调度循环
// 的启停，全部落 `internal/domain/automation`（P2b 第三刀）。
//
// 跨域面用一个端口收口（`automation.Host`）：本域不 import `domain/session`（同层零 import 守卫），
// 所以"建会话/取消/在跑吗/推进事件"四件事由 `automation_host.go` 的适配器提供。
//
// 类型别名（`Rec` / `RunOutcome`）让既有集成用例的调用点几乎零改动——它们读的是域里的**同一份**
// 定义，不会出现"两份定义"这种真正危险的状态。

// automationRec 是域里那份记录的**类型别名**。
type automationRec = automation.Rec

// automationRunOutcome 同上。
type automationRunOutcome = automation.RunOutcome

// automationOrigin 会话来源标记（域里那份的转发）。
//
// TODO(P4b): 删转发（调用点直接依赖 automation 包）
func automationOrigin(id string) string { return automation.Origin(id) }

// TODO(P4b): 删转发（装配侧直接依赖 automation.Plane）
func (s *Service) LoadAutomationDefinitions(dir string) int {
	return s.automationPlane.LoadAutomationDefinitions(dir)
}

// TODO(P4b): 删转发（同上）
func (s *Service) LoadAutomationDefinitionsFromStore(ctx context.Context, dir string) int {
	return s.automationPlane.LoadAutomationDefinitionsFromStore(ctx, dir)
}

// TODO(P4b): 删转发（写面直接依赖 automation.Plane）
func (s *Service) writeAutomation(ctx context.Context, id string, patch map[string]any, create bool) (dtoautomation.AutomationResp, error) {
	return s.automationPlane.WriteAutomation(ctx, id, patch, create)
}

// TODO(P4b): 删转发（同上）
func (s *Service) DeleteAutomationByID(ctx context.Context, id string) (bool, error) {
	return s.automationPlane.DeleteAutomationByID(ctx, id)
}

// TODO(P4b): 删转发（同上）
func (s *Service) AutomationByID(id string) (dtoautomation.AutomationResp, bool) {
	return s.automationPlane.AutomationByID(id)
}

// TODO(P4b): 删转发（同上）
func (s *Service) automationDueSnapshot(now time.Time) []automation.Rec {
	return s.automationPlane.DueSnapshot(now)
}

// TODO(P4b): 删转发（同上）
func (s *Service) automationRecByID(id string) (automation.Rec, bool) {
	return s.automationPlane.RecByID(id)
}

// TODO(P4b): 删转发（同上）
func (s *Service) automationHasActiveRun(rec automation.Rec) bool {
	return s.automationPlane.HasActiveRun(rec)
}

// TODO(P4b): 删转发（同上）
func (s *Service) finishAutomationRun(ctx context.Context, rec automation.Rec, out automation.RunOutcome) automation.RunOutcome {
	return s.automationPlane.FinishAutomationRun(ctx, rec, out)
}

// TODO(P4b): 删转发（同上）
func (s *Service) StartAutomationScheduler(interval time.Duration) {
	s.automationPlane.StartAutomationScheduler(interval)
}

// TODO(P4b): 删转发（同上）
func (s *Service) StopAutomationScheduler() { s.automationPlane.StopAutomationScheduler() }

// ——— 域内动作的测试可达入口（P2b） ———
//
// 下面五条本是域内私有动作（调度 tick、启动 pass、单次触发、运行态 overlay 写回），
// 集成用例要按**真实装配链**驱动它们（真库 + 真事件总线），所以留一层转发。
// 它们是集成测试的驱动面，不是产品接口——P4b 删 manager 时随用例一起处理。

// TODO(P4b): 删转发（调用面直接依赖 automation.Plane）
func (s *Service) automationTick(ctx context.Context, now time.Time) {
	s.automationPlane.Tick(ctx, now)
}

// TODO(P4b): 删转发（同上）
func (s *Service) automationStartupPass(ctx context.Context) { s.automationPlane.StartupPass(ctx) }

// TODO(P4b): 删转发（同上）
func (s *Service) automationRunOnce(ctx context.Context, rec automation.Rec, scheduledFor time.Time, trigger string) automation.RunOutcome {
	return s.automationPlane.RunOnce(ctx, rec, scheduledFor, trigger)
}

// TODO(P4b): 删转发（同上）
func (s *Service) runAutomationNow(ctx context.Context, id string) (automation.RunOutcome, error) {
	return s.automationPlane.RunNow(ctx, id)
}

// TODO(P4b): 删转发（同上）
func (s *Service) saveAutomationOverlay(ctx context.Context, id string, overlay map[string]any, expectedLastFired string) error {
	return s.automationPlane.SaveOverlay(ctx, id, overlay, expectedLastFired)
}

// TODO(P4b): 删转发（同上）
func automationLastFiredText(t *time.Time) string { return automation.LastFiredText(t) }
