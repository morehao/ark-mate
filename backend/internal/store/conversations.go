package store

import (
	"context"
	"database/sql"
	"errors"
)

// conversationColumns 全列查询（scanConversations 用）。
const conversationColumns = `id, agent_id, cwd, workspace_temporary, provider, model, runtime_hint, provider_session_id, title, pinned, archived, status, last_seq, created_at, updated_at`

func scanConversation(row interface{ Scan(...any) error }) (Conversation, error) {
	var s Conversation
	var pinned, archived, workspaceTemporary int
	var created, updated string
	if err := row.Scan(&s.ID, &s.AgentID, &s.Cwd, &workspaceTemporary, &s.Provider, &s.Model, &s.RuntimeHint,
		&s.ProviderSessionID, &s.Title, &pinned, &archived, &s.Status, &s.LastSeq, &created, &updated); err != nil {
		return Conversation{}, err
	}
	s.Pinned = intBool(pinned)
	s.Archived = intBool(archived)
	s.WorkspaceTemporary = intBool(workspaceTemporary)
	s.CreatedAt = parseTS(created)
	s.UpdatedAt = parseTS(updated)
	return s, nil
}

// UpsertConversation 写入（覆盖）一条会话记录。last_seq 采用触顶语义（MAX），避免误清零。
func (s *Store) UpsertConversation(ctx context.Context, sess Conversation) error {
	created := sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.CreatedAt.IsZero() {
		created = nowRFC3339()
	}
	updated := sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.UpdatedAt.IsZero() {
		updated = nowRFC3339()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversations(id, agent_id, cwd, workspace_temporary, provider, model, runtime_hint, provider_session_id, title, pinned, archived, status, last_seq, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			agent_id     = excluded.agent_id,
			cwd          = excluded.cwd,
			workspace_temporary = excluded.workspace_temporary,
			provider     = excluded.provider,
			model        = excluded.model,
			runtime_hint = excluded.runtime_hint,
			provider_session_id = excluded.provider_session_id,
			title        = excluded.title,
			pinned       = excluded.pinned,
			archived     = excluded.archived,
			status       = excluded.status,
			last_seq     = MAX(conversations.last_seq, excluded.last_seq),
			created_at   = excluded.created_at,
			updated_at   = excluded.updated_at`,
		sess.ID, sess.AgentID, sess.Cwd, boolInt(sess.WorkspaceTemporary), sess.Provider, sess.Model, sess.RuntimeHint,
		sess.ProviderSessionID, sess.Title,
		boolInt(sess.Pinned), boolInt(sess.Archived), sess.Status, sess.LastSeq, created, updated)
	return err
}

// GetConversation 读取单个会话。
func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+conversationColumns+` FROM conversations WHERE id = ?`, id)
	sess, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, err
	}
	return sess, true, nil
}

// ListConversations 列出全部会话（updated_at 降序；archived=nil 表示不过滤）。
func (s *Store) ListConversations(ctx context.Context, archived *bool) ([]Conversation, error) {
	q := `SELECT ` + conversationColumns + ` FROM conversations`
	var args []any
	if archived != nil {
		q += ` WHERE archived = ?`
		args = append(args, boolInt(*archived))
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		sess, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateConversationMeta 更新会话元数据子集（title/pinned/archived/status 的 nil 指针 = 不更新；
// updated_at 恒刷新）。
func (s *Store) UpdateConversationMeta(ctx context.Context, id string, meta ConversationMetaPatch) error {
	var set []string
	var args []any
	if meta.Title != nil {
		set = append(set, "title = ?")
		args = append(args, *meta.Title)
	}
	if meta.Pinned != nil {
		set = append(set, "pinned = ?")
		args = append(args, boolInt(*meta.Pinned))
	}
	if meta.Archived != nil {
		set = append(set, "archived = ?")
		args = append(args, boolInt(*meta.Archived))
	}
	if meta.Status != nil {
		set = append(set, "status = ?")
		args = append(args, *meta.Status)
	}
	if meta.Provider != nil {
		set = append(set, "provider = ?")
		args = append(args, *meta.Provider)
	}
	if meta.Model != nil {
		set = append(set, "model = ?")
		args = append(args, *meta.Model)
	}
	if len(set) == 0 {
		return nil
	}
	set = append(set, "updated_at = ?")
	args = append(args, nowRFC3339(), id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET `+joinComma(set)+` WHERE id = ?`, args...)
	return err
}

// ConversationMetaPatch 会话元数据子集更新（nil = 不更新）。
type ConversationMetaPatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
	Status   *string
	Provider *string
	Model    *string
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
