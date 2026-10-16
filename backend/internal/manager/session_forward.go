package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/model"
)

// ——— 会话域：P3d 过渡期的转发层（P3b-1a 先落读模型的物化与重建） ———

// TODO(P4b): 删转发（调用点直接依赖 session 包）
func decodePayload(raw string) any { return session.DecodePayload(raw) }

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
