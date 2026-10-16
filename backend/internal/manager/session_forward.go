package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
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

// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) ListSessions(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error) {
	return s.sessionPlane.ListSessions(archived, cwd, workspaceID)
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
