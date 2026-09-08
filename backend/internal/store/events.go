package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AppendEvent 追加一条事件（facts 唯一写入口的落库侧）：
// 单事务内完成「分配全局 seq（meta.events_last_seq+1）→ 插 events → 更新 session.last_seq」。
// 调用方保证 session 已存在（sessions 表 FK）。
// payload 序列化为 JSON 文本（map/结构体均可——结构体载荷此前不入 transcript，见设计文档修复清单 #10）。
func (s *Store) AppendEvent(ctx context.Context, sessionID, typ string, payload any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("store: marshal payload: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	seq, err := allocSeqTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(seq, session_id, type, payload_json, created_at) VALUES(?, ?, ?, ?, ?)`,
		seq, sessionID, typ, string(b), nowRFC3339()); err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	if err := touchSessionLastSeqTx(ctx, tx, sessionID, seq); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// AppendEventJSON 同 AppendEvent，但载荷已是 JSON 文本（迁移导入与高频写路径复用，免二次序列化）。
func (s *Store) AppendEventJSON(ctx context.Context, sessionID, typ, payloadJSON string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	seq, err := allocSeqTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events(seq, session_id, type, payload_json, created_at) VALUES(?, ?, ?, ?, ?)`,
		seq, sessionID, typ, payloadJSON, nowRFC3339()); err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	if err := touchSessionLastSeqTx(ctx, tx, sessionID, seq); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// allocSeqTx 在事务内分配下一个全局 seq（meta 计数行 +1，写回）。
func allocSeqTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var cur int64
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaKeySeq).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: read seq meta: %w", err)
	}
	cur++
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaKeySeq, fmt.Sprintf("%d", cur)); err != nil {
		return 0, fmt.Errorf("store: bump seq meta: %w", err)
	}
	return cur, nil
}

// touchSessionLastSeqTx 抬高会话水位（幂等：仅当传入 seq 更大时更新）。
func touchSessionLastSeqTx(ctx context.Context, tx *sql.Tx, sessionID string, seq int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET last_seq = ?, updated_at = ?
		 WHERE id = ? AND last_seq < ?`,
		seq, nowRFC3339(), sessionID, seq)
	if err != nil {
		return fmt.Errorf("store: touch session last_seq: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// session 不存在（FK 已拦）或 last_seq 已更大 → 前者由 FK 报错兜底；这里不额外报错。
	}
	return nil
}

// EventsAfter 返回某会话 seq > after 的事件（升序，limit 上限；hasMore 表示仍有余量）。
// 服务端事件回放（history 续传/断线补偿）从 events 表读，不再受内存环缓冲淘汰限制。
func (s *Store) EventsAfter(ctx context.Context, sessionID string, after, limit int64) ([]Event, bool, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, session_id, type, payload_json, created_at FROM events
		 WHERE session_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		sessionID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		var created string
		if err := rows.Scan(&e.Seq, &e.SessionID, &e.Type, &e.PayloadJSON, &created); err != nil {
			return nil, false, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := int64(len(out)) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// EventCount 统计某会话/全局事件行数（迁移校验用；sessionID 空 = 全库）。
func (s *Store) EventCount(ctx context.Context, sessionID string) (int64, error) {
	var n int64
	if sessionID == "" {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
		return n, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE session_id = ?`, sessionID).Scan(&n)
	return n, err
}

// EventsAllAfter 全表按 seq 升序游标分页（跨会话；重启恢复注入 bus 用，保证全局号序升序回放）。
func (s *Store) EventsAllAfter(ctx context.Context, after, limit int64) ([]Event, bool, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, session_id, type, payload_json, created_at FROM events
		 WHERE seq > ? ORDER BY seq ASC LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		var created string
		if err := rows.Scan(&e.Seq, &e.SessionID, &e.Type, &e.PayloadJSON, &created); err != nil {
			return nil, false, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := int64(len(out)) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}
