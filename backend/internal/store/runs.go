package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
)

// ——— runs 表（一轮 prompt 的物化行；turn 收口时由 Service.materializeRun 写入） ———

// CreateRun 插入一条 run（幂等：同 id 已存在返回 nil）。
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	r.StartedAt = nonZeroTime(r.StartedAt)
	return s.runs.CreateIfAbsent(ctx, &r)
}

// FinishRun 更新 run 终态（status/stop_reason/error_code/usage/ended_at）。
func (s *Store) FinishRun(ctx context.Context, id string, status, stopReason, errorCode, usageJSON string, endedAt time.Time) error {
	// 零值 → 当前时刻（与历史实现一致：未显式给终态时间时按「此刻结束」记）。
	return s.runs.Finish(ctx, id, status, stopReason, errorCode, usageJSON, model.FormatTimeOrNow(endedAt))
}

// ListRuns 列出某会话全部 run（started_at 升序；失败轮重试/统计按此序回溯）。
func (s *Store) ListRuns(ctx context.Context, conversationID string) ([]Run, error) {
	return s.runs.List(ctx, conversationID)
}

// RunLastSeq 返回某会话最后一条 run 的 last_seq（0 = 尚无 run；供下一 run 的事件窗口起点）。
func (s *Store) RunLastSeq(ctx context.Context, conversationID string) (int64, error) {
	return s.runs.LastSeq(ctx, conversationID)
}
