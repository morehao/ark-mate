package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ImportItem 是一次会话导入的输入（transcript 文件内容）：会话元数据 + 事件原语。
type ImportItem struct {
	Conversation Conversation
	Events       []Event // PayloadJSON 已序列化；Seq 忽略（事务内重分配全局 seq）
}

// ImportBatch 在一个事务内导入一批会话（transcript → DB，P1 迁移关键路径）：
// 分配全局 seq → 插 events → upsert conversations（水位=本会话末条 seq）→ 校验 → 设导入标记 → 提交。
// 任一步失败整体回滚（staging 语义）；marker 与库内 imported_transcripts 一致时幂等返回 ErrAlreadyImported。
// expectedFiles/expectedEvents > 0 时做导入前后数量校验（防止漏行/错位）。
func (s *Store) ImportBatch(ctx context.Context, items []ImportItem, marker string, expectedFiles, expectedEvents int) (int64, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if marker != "" {
		cur, ok, err := s.getMetaTx(ctx, tx, metaKeyImportMarker)
		if err != nil {
			return 0, 0, err
		}
		if ok && cur == marker {
			return 0, 0, ErrAlreadyImported
		}
	}

	var totalEvents int64
	conversationsDone := 0
	for _, item := range items {
		if item.Conversation.ID == "" {
			return 0, 0, fmt.Errorf("store: import item missing conversation id")
		}
		// 先建会话（events 表 FK 引用 conversations），再插事件，最后抬会话水位。
		if err := upsertConversationTx(ctx, tx, item.Conversation); err != nil {
			return 0, 0, err
		}
		var lastSeq int64
		for _, ev := range item.Events {
			seq, err := allocSeqTx(ctx, tx)
			if err != nil {
				return 0, 0, err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO events(seq, conversation_id, type, payload_json, created_at) VALUES(?, ?, ?, ?, ?)`,
				seq, item.Conversation.ID, ev.Type, ev.PayloadJSON, nowRFC3339()); err != nil {
				return 0, 0, fmt.Errorf("store: import event: %w", err)
			}
			lastSeq = seq
			totalEvents++
		}
		if lastSeq > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE conversations SET last_seq = ?, updated_at = ? WHERE id = ?`,
				lastSeq, nowRFC3339(), item.Conversation.ID); err != nil {
				return 0, 0, fmt.Errorf("store: import conversation last_seq: %w", err)
			}
		}
		conversationsDone++
	}

	if expectedFiles > 0 && conversationsDone != expectedFiles {
		return 0, 0, fmt.Errorf("store: import validation failed: files %d != expected %d", conversationsDone, expectedFiles)
	}
	if expectedEvents > 0 && int(totalEvents) != expectedEvents {
		return 0, 0, fmt.Errorf("store: import validation failed: events %d != expected %d", totalEvents, expectedEvents)
	}

	if marker != "" {
		if err := s.setMetaTx(ctx, tx, metaKeyImportMarker, marker); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return totalEvents, conversationsDone, nil
}

// upsertConversationTx 事务内 upsert 会话（last_seq 触顶语义）。
func upsertConversationTx(ctx context.Context, tx *sql.Tx, sess Conversation) error {
	created := sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.CreatedAt.IsZero() {
		created = nowRFC3339()
	}
	updated := sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if sess.UpdatedAt.IsZero() {
		updated = nowRFC3339()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversations(id, agent_id, cwd, workspace_temporary, title, pinned, archived, status, last_seq, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			agent_id   = excluded.agent_id,
			cwd        = excluded.cwd,
			workspace_temporary = excluded.workspace_temporary,
			title      = excluded.title,
			pinned     = excluded.pinned,
			archived   = excluded.archived,
			status     = excluded.status,
			last_seq   = MAX(conversations.last_seq, excluded.last_seq),
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		sess.ID, sess.AgentID, sess.Cwd, boolInt(sess.WorkspaceTemporary), sess.Title,
		boolInt(sess.Pinned), boolInt(sess.Archived), sess.Status, sess.LastSeq, created, updated)
	if err != nil {
		return fmt.Errorf("store: upsert conversation %q: %w", sess.ID, err)
	}
	return nil
}
