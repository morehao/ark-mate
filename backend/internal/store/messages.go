package store

import (
	"context"
	"strings"
)

// ——— messages 表（可重建读模型：轮内消息物化；events 仍为唯一事实源） ———

// 行结构 Message 定义于 store.go（含 ID）。

// CreateMessages 单事务批量插入一轮的 messages 行（可重复调用：同 run_id 先删后插，
// 保证「从 events 重建」幂等）。
func (s *Store) CreateMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE run_id = ?`, msgs[0].RunID); err != nil {
		return err
	}
	for _, m := range msgs {
		at := m.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO messages(conversation_id, run_id, role, text, events_json, tool_details_json,
			                     seq_lo, seq_hi, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ConversationID, m.RunID, m.Role, m.Text, m.EventsJSON, m.ToolDetailsJSON,
			m.SeqLo, m.SeqHi, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MessageTextHit 一条命中（搜索片段与计数用）。
type MessageTextHit struct {
	ConversationID string
	Role           string
	Text           string
}

// SearchUserMessages 在指定会话的 user 消息中做子串检索（大小写不敏感；LIKE 通配转义），
// 返回全部命中行（service 侧折叠片段/计数）。
func (s *Store) SearchUserMessages(ctx context.Context, conversationID, q string) ([]MessageTextHit, error) {
	like := "%" + escapeLike(q) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT conversation_id, role, text FROM messages
		WHERE conversation_id = ? AND role = 'user' AND text LIKE ? ESCAPE '\'
		ORDER BY seq_lo ASC`, conversationID, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageTextHit{}
	for rows.Next() {
		var h MessageTextHit
		if err := rows.Scan(&h.ConversationID, &h.Role, &h.Text); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// ListMessages 列出某会话全部消息行（seq 序；测试与后续 REST 用）。
func (s *Store) ListMessages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, run_id, role, text, events_json, tool_details_json,
		       seq_lo, seq_hi, created_at
		FROM messages WHERE conversation_id = ? ORDER BY seq_lo ASC, id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var at string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Role, &m.Text,
			&m.EventsJSON, &m.ToolDetailsJSON, &m.SeqLo, &m.SeqHi, &at); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(at)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
