package store

import (
	"context"
	"database/sql"
	"errors"
)

// sessionColumns 全列查询（scanSessions 用）。
const sessionColumns = `id, agent_id, runtime_id, cwd, title, pinned, archived, status, last_seq, created_at, updated_at`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var pinned, archived int
	var created, updated string
	if err := row.Scan(&s.ID, &s.AgentID, &s.RuntimeID, &s.Cwd, &s.Title,
		&pinned, &archived, &s.Status, &s.LastSeq, &created, &updated); err != nil {
		return Session{}, err
	}
	s.Pinned = intBool(pinned)
	s.Archived = intBool(archived)
	s.CreatedAt = parseTS(created)
	s.UpdatedAt = parseTS(updated)
	return s, nil
}

// UpsertSession 写入（覆盖）一条会话记录。last_seq 采用触顶语义（MAX），避免误清零。
func (s *Store) UpsertSession(ctx context.Context, sess Session) error {
	created := sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.CreatedAt.IsZero() {
		created = nowRFC3339()
	}
	updated := sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.UpdatedAt.IsZero() {
		updated = nowRFC3339()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(id, agent_id, runtime_id, cwd, title, pinned, archived, status, last_seq, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			agent_id   = excluded.agent_id,
			runtime_id = excluded.runtime_id,
			cwd        = excluded.cwd,
			title      = excluded.title,
			pinned     = excluded.pinned,
			archived   = excluded.archived,
			status     = excluded.status,
			last_seq   = MAX(sessions.last_seq, excluded.last_seq),
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		sess.ID, sess.AgentID, sess.RuntimeID, sess.Cwd, sess.Title,
		boolInt(sess.Pinned), boolInt(sess.Archived), sess.Status, sess.LastSeq, created, updated)
	return err
}

// GetSession 读取单个会话。
func (s *Store) GetSession(ctx context.Context, id string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return sess, true, nil
}

// ListSessions 列出全部会话（updated_at 降序；archived=nil 表示不过滤）。
func (s *Store) ListSessions(ctx context.Context, archived *bool) ([]Session, error) {
	q := `SELECT ` + sessionColumns + ` FROM sessions`
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
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
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

// UpdateSessionMeta 更新会话元数据子集（title/pinned/archived/status 的 nil 指针 = 不更新；
// updated_at 恒刷新）。
func (s *Store) UpdateSessionMeta(ctx context.Context, id string, meta SessionMetaPatch) error {
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
	if len(set) == 0 {
		return nil
	}
	set = append(set, "updated_at = ?")
	args = append(args, nowRFC3339(), id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET `+joinComma(set)+` WHERE id = ?`, args...)
	return err
}

// SessionMetaPatch 会话元数据子集更新（nil = 不更新）。
type SessionMetaPatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
	Status   *string
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
