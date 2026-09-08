package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// migration 是一次原子 schema 变更：其 statements 在单个事务内顺序执行。
type migration struct {
	version    int
	name       string
	statements []string
}

// schemaMigrations 是版本-迁移表（有序；追加迁移 = 在尾部新增，绝不回改历史）。
// v1 baseline：meta / conversations / events / runs / messages / permission_memories + 索引。
var schemaMigrations = []migration{
	{
		version: 1,
		name:    "baseline: meta/conversations/events/runs/messages/permission_memories",
		statements: []string{
			`CREATE TABLE meta (
				key   TEXT PRIMARY KEY,
				value TEXT NOT NULL
			)`,
			`CREATE TABLE conversations (
				id         TEXT PRIMARY KEY,
				agent_id   TEXT NOT NULL,
				cwd        TEXT NOT NULL DEFAULT '',
				title      TEXT NOT NULL DEFAULT '',
				pinned     INTEGER NOT NULL DEFAULT 0,
				archived   INTEGER NOT NULL DEFAULT 0,
				status     TEXT NOT NULL DEFAULT 'in_progress',
				last_seq   INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX idx_conversations_list ON conversations(archived, updated_at)`,
			`CREATE TABLE events (
				seq          INTEGER PRIMARY KEY,
				conversation_id   TEXT NOT NULL REFERENCES conversations(id),
				type         TEXT NOT NULL,
				payload_json TEXT NOT NULL DEFAULT '',
				created_at   TEXT NOT NULL
			)`,
			`CREATE INDEX idx_events_conversation ON events(conversation_id, seq)`,
			`CREATE TABLE runs (
				id          TEXT PRIMARY KEY,
				conversation_id  TEXT NOT NULL REFERENCES conversations(id),
				user_text   TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'running',
				error_code  TEXT NOT NULL DEFAULT '',
				stop_reason TEXT NOT NULL DEFAULT '',
				usage_json  TEXT NOT NULL DEFAULT '',
				first_seq   INTEGER NOT NULL DEFAULT 0,
				last_seq    INTEGER NOT NULL DEFAULT 0,
				started_at  TEXT NOT NULL,
				ended_at    TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX idx_runs_conversation ON runs(conversation_id, started_at)`,
			`CREATE TABLE messages (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				conversation_id        TEXT NOT NULL REFERENCES conversations(id),
				run_id            TEXT NOT NULL DEFAULT '',
				role              TEXT NOT NULL,
				text              TEXT NOT NULL DEFAULT '',
				events_json       TEXT NOT NULL DEFAULT '',
				tool_details_json TEXT NOT NULL DEFAULT '',
				seq_lo            INTEGER NOT NULL DEFAULT 0,
				seq_hi            INTEGER NOT NULL DEFAULT 0,
				created_at        TEXT NOT NULL
			)`,
			`CREATE INDEX idx_messages_conversation ON messages(conversation_id, run_id)`,
			`CREATE TABLE permission_memories (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				conversation_id TEXT NOT NULL REFERENCES conversations(id),
				title      TEXT NOT NULL,
				kind       TEXT NOT NULL DEFAULT '',
				decision   TEXT NOT NULL,
				created_at TEXT NOT NULL,
				UNIQUE(conversation_id, title, kind)
			)`,
			`CREATE INDEX idx_memories_conversation ON permission_memories(conversation_id)`,
		},
	},
}

// latestSchemaVersion 当前支持的最新 schema 版本。
func latestSchemaVersion() int {
	return schemaMigrations[len(schemaMigrations)-1].version
}

// migrate 幂等应用未执行的迁移（自管 schema_migrations：单事务 DDL + 记录版本）。
// 库版本高于本二进制（未知未来版本）→ 拒绝启动（对齐 zacp refuse-start）。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	var maxVersion int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan schema_migrations: %w", err)
		}
		applied[v] = true
		if v > maxVersion {
			maxVersion = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: iterate schema_migrations: %w", err)
	}
	if maxVersion > latestSchemaVersion() {
		return fmt.Errorf("store: schema v%d 高于本二进制支持的 v%d（请升级二进制，拒绝启动）", maxVersion, latestSchemaVersion())
	}

	var pending []int
	for _, m := range schemaMigrations {
		if !applied[m.version] {
			pending = append(pending, m.version)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	sort.Ints(pending)

	for _, v := range pending {
		var m migration
		for _, mm := range schemaMigrations {
			if mm.version == v {
				m = mm
				break
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration v%d: %w", v, err)
		}
		for _, stmt := range m.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store: apply migration v%d (%s): %w", v, m.name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
			v, m.name, nowRFC3339()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record migration v%d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration v%d: %w", v, err)
		}
	}
	return nil
}

// SchemaVersion 返回库当前 schema 版本（0 = 空库/未迁移）。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return v, nil
}
