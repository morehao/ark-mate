// Package svcusage 定义用量统计面的服务端口。
package svcusage

import (
	"context"

	"github.com/morehao/ark-forge/internal/dto/dtousage"
)

// UsageSvc 用量统计面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ UsageSvc = (*T)(nil) 保证一致性。
type UsageSvc interface {
	// UsageSummary 聚合用量统计（M3 首项：runs 计数/时长 + 会话上下文/费用快照）。
	UsageSummary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error)
}
