package store

import (
	"context"
	"errors"
	"time"
)

// ——— runs 表（一轮 prompt 的物化行；turn 收口时由 Service.materializeRun 写入） ———

// CreateRun 插入一条 run（幂等：同 id 已存在返回 nil）。
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	started := r.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if r.StartedAt.IsZero() {
		started = nowRFC3339()
	}
	ended := ""
	if !r.EndedAt.IsZero() {
		ended = r.EndedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO runs(id, conversation_id, user_text, status, error_code, stop_reason, usage_json,
		                           first_seq, last_seq, first_token_ms, started_at, ended_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ConversationID, r.UserText, r.Status, r.ErrorCode, r.StopReason, r.UsageJSON,
		r.FirstSeq, r.LastSeq, r.FirstTokenMS, started, ended)
	return err
}

// FinishRun 更新 run 终态（status/stop_reason/error_code/usage/ended_at）。
func (s *Store) FinishRun(ctx context.Context, id string, status, stopReason, errorCode, usageJSON string, endedAt time.Time) error {
	ended := nowRFC3339()
	if !endedAt.IsZero() {
		ended = endedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, stop_reason = ?, error_code = ?, usage_json = ?, ended_at = ?
		 WHERE id = ?`, status, stopReason, errorCode, usageJSON, ended, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.New("store: run not found")
	}
	return nil
}

// ListRuns 列出某会话全部 run（started_at 升序；失败轮重试/统计按此序回溯）。
func (s *Store) ListRuns(ctx context.Context, conversationID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, user_text, status, error_code, stop_reason, usage_json,
		       first_seq, last_seq, first_token_ms, started_at, ended_at
		FROM runs WHERE conversation_id = ? ORDER BY started_at ASC, first_seq ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var started, ended string
		if err := rows.Scan(&r.ID, &r.ConversationID, &r.UserText, &r.Status, &r.ErrorCode, &r.StopReason,
			&r.UsageJSON, &r.FirstSeq, &r.LastSeq, &r.FirstTokenMS, &started, &ended); err != nil {
			return nil, err
		}
		r.StartedAt = parseTS(started)
		r.EndedAt = parseTS(ended)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RunLastSeq 返回某会话最后一条 run 的 last_seq（0 = 尚无 run；供下一 run 的事件窗口起点）。
func (s *Store) RunLastSeq(ctx context.Context, conversationID string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(last_seq), 0) FROM runs WHERE conversation_id = ?`, conversationID).Scan(&v)
	return v, err
}
