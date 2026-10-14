package store

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
)

// ——— runs 表（一轮 prompt 的**实体**：v22 起开轮落行，终态 UPDATE） ———

// CreateRun 插入一条 run 行（**开轮**语义见 manager.openRun：status=running、first_seq=本轮
// `run.started` 的位次、last_seq=-1）。幂等：同 id 或同 `(session_id, first_seq)` 已存在即跳过。
//
// 刻意只做"行级插入"、不在这里塞业务语义：本函数也被测试用来直接种一行终态（那时它就是
// 一条普通插入）。"开轮该写什么"属于 manager。
func (s *Store) CreateRun(ctx context.Context, r model.RunEntity) error {
	r.StartedAt = nonZeroTime(r.StartedAt)
	return s.runs.CreateIfAbsent(ctx, &r)
}

// RunByID 按 id 取一条 run；不存在 → (零值, false, nil)。
func (s *Store) RunByID(ctx context.Context, id string) (model.RunEntity, bool, error) {
	return s.runs.GetByID(ctx, id)
}

// RunningRuns 取某会话进行中的 run 行（first_seq 降序，最多两条；终态物化的兜底路径用）。
//
// 返回一页而不是一条：多于一条意味着崩溃残轮，"最近一条"只是猜测——调用方要能选择不猜。
func (s *Store) RunningRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	return s.runs.RunningRows(ctx, sessionID)
}

// FinishRun 更新 run 收口列（窗口右端 + 终态 + 首令牌耗时）。
func (s *Store) FinishRun(ctx context.Context, id string, p model.RunFinishPatch) error {
	return s.runs.Finish(ctx, id, p)
}

// ListRuns 列出某会话全部 run（started_at 升序；失败轮重试/统计按此序回溯）。
//
// 排序刻意不动：进行中的行（v22 起在库里）`started_at` 就是开轮时刻，天然排在正确位置。
func (s *Store) ListRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	return s.runs.List(ctx, sessionID)
}
