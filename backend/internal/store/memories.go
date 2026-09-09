package store

import (
	"context"
)

// AddMemory 写入一条会话级权限记忆（同 conversation+title+kind 已存在时覆盖 decision；
// 同侧重复授权不刷新授予时间——与 PermissionManager.Remember 语义对齐）。
func (s *Store) AddMemory(ctx context.Context, m Memory) error {
	created := m.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if m.CreatedAt.IsZero() {
		created = nowRFC3339()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO permission_memories(conversation_id, title, kind, decision, created_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id, title, kind) DO UPDATE SET
			decision   = excluded.decision,
			created_at = CASE WHEN permission_memories.decision = excluded.decision
			                 THEN permission_memories.created_at ELSE excluded.created_at END`,
		m.ConversationID, m.Title, m.Kind, m.Decision, created)
	return err
}

// ListMemories 列出某会话全部权限记忆（授予时间升序，decision 区分 allow/reject）。
func (s *Store) ListMemories(ctx context.Context, conversationID string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, title, kind, decision, created_at
		FROM permission_memories WHERE conversation_id = ? ORDER BY created_at ASC, id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		var created string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Title, &m.Kind, &m.Decision, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteMemory 撤销一条记忆（conversation+title+kind 精确匹配，不区分 decision）；返回是否删除。
func (s *Store) DeleteMemory(ctx context.Context, conversationID, title, kind string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM permission_memories WHERE conversation_id = ? AND title = ? AND kind = ?`,
		conversationID, title, kind)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DeleteConversationMemories 一键撤销整会话全部权限记忆。
func (s *Store) DeleteConversationMemories(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM permission_memories WHERE conversation_id = ?`, conversationID)
	return err
}

// CountConversations 会话行数（迁移校验用）。
func (s *Store) CountConversations(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations`).Scan(&n)
	return n, err
}

// ListAllMemories 列出全库权限记忆（sqlite 启动恢复：一次装载所有会话的 allow/reject 记忆）。
func (s *Store) ListAllMemories(ctx context.Context) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, title, kind, decision, created_at
		FROM permission_memories ORDER BY conversation_id ASC, created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		var created string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Title, &m.Kind, &m.Decision, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
