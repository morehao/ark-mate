package svcusage

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
)

// UsageSvc 用量统计用例端口。
//
// v3.1 §3.2.1：控制器不得 import `internal/domain`。用量统计是读模型，校验、分组口径与响应
// 装配全在域平面里（见 `domain/session/usage.go`），这里只复述一个方法。
type UsageSvc interface {
	Summary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error)
}

type usageSvc struct{ plane *session.Plane }

// New 构造用量统计用例。
func New(plane *session.Plane) UsageSvc { return &usageSvc{plane: plane} }

func (s *usageSvc) Summary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error) {
	return s.plane.UsageSummary(ctx, q)
}
