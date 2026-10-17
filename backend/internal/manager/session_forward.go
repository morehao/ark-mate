package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/eventbus"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
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
func (s *Service) sessionRec(id string) *SessionRec { return s.sessionPlane.Rec(id) }

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) bumpGeneration(id string) int64 { return s.sessionPlane.BumpGeneration(id) }

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func decodePayload(raw string) any { return session.DecodePayload(raw) }

// TODO(P4b): 删转发（`publish.go` 直接依赖 session.Plane）
//
// `materializeRun` 是事件流收口时把 runs 行写成终态的那一步（`publish.go` 调它）。
func (s *Service) materializeRun(sessionID, typ string, payload map[string]any, terminalSeq int64) {
	s.sessionPlane.MaterializeRun(sessionID, typ, payload, terminalSeq)
}

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

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) persistMemoryAdd(it interaction.MemoryItem) {
	s.sessionPlane.PersistMemoryAdd(it.SessionID(), it.Title, it.Kind, it.Decision,
		it.Scope, it.GrantInputDigest, it.Since)
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
func (s *Service) listQueuedInputs(ctx context.Context, convID string) []session.QueuedInput {
	return s.sessionPlane.ListQueuedInputsOf(ctx, convID)
}

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) staleQueuedInputsOnRestore(ctx context.Context) {
	s.sessionPlane.StaleQueuedInputsOnRestore(ctx)
}
