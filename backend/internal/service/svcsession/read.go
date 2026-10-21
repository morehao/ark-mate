package svcsession

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/contract/dtosession"
)

// ReadSvc 会话**只读列表面**（v3.1 §3.2.1：控制器不得 import `internal/domain`）。
//
// 与 `HistorySvc`/`DetailSvc` 的分界：那两面要请求校验与错误码出口；这一面是纯读模型
// （列表/搜索/待投递/轮次），域里已折好 DTO，用例层只复述。
type ReadSvc interface {
	List(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error)
	Search(ctx context.Context, q string, limit int) ([]dtosession.SessionSearchHit, error)
	QueuedInputs(ctx context.Context, id string) ([]dtosession.QueuedInputResp, error)
	Runs(ctx context.Context, id string) ([]dtosession.RunResp, error)
}

type readSvc struct{ plane *session.Plane }

// NewRead 构造会话只读列表面用例。
func NewRead(plane *session.Plane) ReadSvc { return &readSvc{plane: plane} }

func (s *readSvc) List(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error) {
	items, err := s.plane.ListSessions(archived, cwd, workspaceID)
	if err != nil {
		return nil, err
	}
	return sessionRespsDTO(items), nil
}

func (s *readSvc) Search(ctx context.Context, q string, limit int) ([]dtosession.SessionSearchHit, error) {
	items, err := s.plane.SearchSessions(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return sessionSearchHitsDTO(items), nil
}

func (s *readSvc) QueuedInputs(ctx context.Context, id string) ([]dtosession.QueuedInputResp, error) {
	items, err := s.plane.ListQueuedInputs(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]dtosession.QueuedInputResp, 0, len(items))
	for _, q := range items {
		out = append(out, dtosession.QueuedInputResp{
			ID: q.ID, Text: q.Text, Delivery: q.Delivery,
			TargetRunID: q.TargetRunID, Generation: q.Generation, CreatedAt: q.CreatedAt,
		})
	}
	return out, nil
}

func (s *readSvc) Runs(ctx context.Context, id string) ([]dtosession.RunResp, error) {
	items, err := s.plane.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	return runRespsDTO(items), nil
}
