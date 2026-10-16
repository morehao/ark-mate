package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/eventbus"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosearch"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/internal/model"
)

// ——— 会话域：过渡期的转发层（P4b 删） ———

// SessionRec 是会话记录的**类型别名**（不是复制：全仓只有域里那一份定义）。
//
// 用别名而不是大规模改名：~50 处 `*SessionRec` 调用点一个字都不用改，而"两份定义"这种
// 真正危险的状态也不会出现——别名与域里的类型**是同一个类型**，域改字段它跟着改。
type SessionRec = session.SessionRec

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) registerSession(rec *SessionRec) { s.sessionPlane.RegisterSession(rec) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) markSession(id, status string) { s.sessionPlane.MarkSession(id, status) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) hasSession(id string) bool { return s.sessionPlane.HasSession(id) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) sessionRec(id string) *SessionRec { return s.sessionPlane.Rec(id) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) bumpGeneration(id string) int64 { return s.sessionPlane.BumpGeneration(id) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) sessionGeneration(id string) int64 { return s.sessionPlane.SessionGeneration(id) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) sessionResp(id string) dtosession.SessionResp {
	return s.sessionPlane.SessionResp(id)
}

// TODO(P4b): 删转发（同上）
func (s *Service) SearchMessages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error) {
	return s.sessionPlane.SearchMessages(ctx, q, limit)
}

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func decodePayload(raw string) any { return session.DecodePayload(raw) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
//
// UsageSummary 用量统计（读 `runs` 读模型）。
func (s *Service) UsageSummary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error) {
	return s.sessionPlane.UsageSummary(ctx, q)
}

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func terminalRunStatus(typ string) string { return session.TerminalRunStatus(typ) }

// TODO(P4b): 删转发（`publish.go` 直接依赖 session.Plane）
//
// `materializeRun` 是事件流收口时把 runs 行写成终态的那一步（`publish.go` 调它）。
func (s *Service) materializeRun(sessionID, typ string, payload map[string]any, terminalSeq int64) {
	s.sessionPlane.MaterializeRun(sessionID, typ, payload, terminalSeq)
}

// TODO(P4b): 删转发（会话面直接依赖 session.Plane）
func (s *Service) replaceSessionMessages(ctx context.Context, sessionID string, all []model.EventEntity, runs []model.RunEntity) error {
	return s.sessionPlane.ReplaceSessionMessages(ctx, sessionID, all, runs)
}

// TODO(P4b): 删转发（会话面直接依赖 session.Plane）
func (s *Service) materializeMessages(sessionID string) {
	s.sessionPlane.MaterializeMessages(sessionID)
}

// TODO(P4b): 删转发（会话面直接依赖 session.Plane）
func (s *Service) rebuildSessionMessages(ctx context.Context, sessionID string) error {
	return s.sessionPlane.RebuildSessionMessages(ctx, sessionID)
}

// TODO(P4b): 删转发（会话面直接依赖 session.Plane）
func (s *Service) sessionEvents(ctx context.Context, sessionID string) ([]model.EventEntity, error) {
	return s.sessionPlane.SessionEvents(ctx, sessionID)
}

// TODO(P4b): 删转发（会话面直接依赖 session.Plane）
func (s *Service) sessionRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	return s.sessionPlane.SessionRuns(ctx, sessionID)
}

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func normalizeWorkspaceFilter(cwd string) string { return session.NormalizeWorkspaceFilter(cwd) }

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func deriveTitle(text string) string { return session.DeriveTitle(text) }

// TODO(P4b): 删转发（`GetSession`/`UpdateSession` 还在 manager，随 P3b-2/P4a 走）
func (s *Service) toSessionResp(r *SessionRec) dtosession.SessionResp {
	return s.sessionPlane.ToSessionRespOf(r)
}

// TODO(P4b): 删转发（轮次与权限记忆的落库面直接依赖 session.Plane）
func (s *Service) openRun(sessionID, runID, text string, firstSeq int64) {
	s.sessionPlane.OpenRun(sessionID, runID, text, firstSeq)
}

// TODO(P4b): 删转发（原生恢复兜底面直接依赖 session.Plane）
//
// HandoffMemo 读的是**会话域的事件面**（`pl.bus`），P3b-2b 随会话域搬走。
func (s *Service) handoffMemo(sessionID string, maxChars int) string {
	return s.sessionPlane.HandoffMemo(sessionID, maxChars)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) persistMemoryAdd(sessionID, title, kind, decision string, since time.Time) {
	s.sessionPlane.PersistMemoryAdd(sessionID, title, kind, decision, since)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) persistMemoryDel(sessionID, title, kind string) {
	s.sessionPlane.PersistMemoryDel(sessionID, title, kind)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) persistMemoryDelAll(sessionID string) {
	s.sessionPlane.PersistMemoryDelAll(sessionID)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) memoryRows(ctx context.Context) ([]model.PermissionMemoryEntity, error) {
	return s.sessionPlane.MemoryRows(ctx)
}

// TODO(P4b): 删转发（启动恢复面直接依赖 session.Plane）
func (s *Service) RestoreFromStore(ctx context.Context) (int, int, error) {
	return s.sessionPlane.Restore(ctx)
}

// TODO(P4b): 删转发（模型引用回填面直接依赖 session.Plane）
func (s *Service) backfillSessionModelRefs(ctx context.Context) {
	s.sessionPlane.BackfillSessionModelRefs(ctx)
}

// TODO(P4b): 删转发（同上：它是回填用的同一份换算表）
func (s *Service) runtimeValueToModelID(ctx context.Context, runtimeID string) map[string]string {
	return s.sessionPlane.RuntimeValueToModelID(ctx, runtimeID)
}

// ——— 事件出口的过渡期转发（P4b 删） ———

// TODO(P4b): 删转发（写入面直接依赖 session.Plane）
func (s *Service) publish(typ, sessionID string, payload any) eventbus.Event {
	return s.sessionPlane.Publish(typ, sessionID, payload)
}

// TODO(P4b): 删转发（同上）
func (s *Service) publishRaw(typ, sessionID string, payload any) eventbus.Event {
	return s.sessionPlane.PublishRaw(typ, sessionID, payload)
}

// TODO(P4b): 删转发（同上）
func (s *Service) flushChunks() { s.sessionPlane.FlushChunks() }

// ——— 输入队列的过渡期别名/转发（P4b 删） ———

// QueuedInput 是域里那份队列项的**类型别名**（全仓只有一份定义）。
type QueuedInput = session.QueuedInput

// PromptOutcome 同上。
type PromptOutcome = session.PromptOutcome

// 投递模式（值来自域，别名保证只有一处定义）。
const (
	DeliveryReject              = session.DeliveryReject
	DeliveryAfterRun            = session.DeliveryAfterRun
	DeliveryInterruptThenPrompt = session.DeliveryInterruptThenPrompt
)

// 队列项终态（值来自域，别名保证只有一处定义）。
const (
	ReasonRunFinished = session.ReasonRunFinished
	ReasonUserCancel  = session.ReasonUserCancel
	ReasonDeliverFail = session.ReasonDeliverFail
	ReasonStale       = session.ReasonStale
)

// ValidDeliveries 合法投递模式（域里那份的别名视图）。
var ValidDeliveries = session.ValidDeliveries

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func NormalizeDelivery(v string) (string, error) { return session.NormalizeDelivery(v) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) enqueueInput(ctx context.Context, convID, text, delivery, targetRun string, generation int64) (QueuedInput, error) {
	return s.sessionPlane.EnqueueInput(ctx, convID, text, delivery, targetRun, generation)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) listQueuedInputs(ctx context.Context, convID string) []QueuedInput {
	return s.sessionPlane.ListQueuedInputsOf(ctx, convID)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) claimNextInput(ctx context.Context, convID string, generation int64) (*QueuedInput, bool) {
	return s.sessionPlane.ClaimNextInput(ctx, convID, generation)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) deleteQueuedInput(ctx context.Context, convID, inputID string) (bool, error) {
	return s.sessionPlane.DeleteQueuedInput(ctx, convID, inputID)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) staleQueuedInputsOnRestore(ctx context.Context) {
	s.sessionPlane.StaleQueuedInputsOnRestore(ctx)
}
