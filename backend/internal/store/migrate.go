package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/morehao/ark-forge/internal/model"
	"gorm.io/gorm"
)

// migration 是一次原子 schema 变更：其 statements 在单个事务内顺序执行。
type migration struct {
	version    int
	name       string
	statements []string
}

// schemaMigrations 是版本-迁移表（有序；追加迁移 = 在尾部新增，绝不回改历史）。
// v1 baseline：meta / conversations / events / runs / messages / permission_memories + 索引。
//
// 这里刻意不用 gorm 的 AutoMigrate：AutoMigrate 只做「往目标结构收敛」，
// 表达不了本仓要求的三件事——严格有序的版本号、库版本高于二进制时**拒绝启动**、
// 以及导入路径的 staging 回滚。列定义因此仍与 internal/model 的实体手工保持一致
// （改列时两处必须同步，archtest 会守住实体表名）。
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
				id           TEXT PRIMARY KEY,
				agent_id     TEXT NOT NULL,
				cwd          TEXT NOT NULL DEFAULT '',
				runtime_hint TEXT NOT NULL DEFAULT '',
				title        TEXT NOT NULL DEFAULT '',
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
	{
		version: 2,
		name:    "conversations.provider_session_id（P3 双 id：平台 uuid 会话 id + 原生 provider 列）",
		statements: []string{
			`ALTER TABLE conversations ADD COLUMN provider_session_id TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 3,
		name:    "runs.first_token_ms（单轮首令牌耗时，turn 收口由 materializeRun 填）",
		statements: []string{
			`ALTER TABLE runs ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		version: 4,
		name:    "conversations.provider/model（会话级模型选择）",
		statements: []string{
			`ALTER TABLE conversations ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE conversations ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 5,
		name:    "conversations.workspace_temporary（工作空间是否平台代建：权威标志，不从路径形状反推）",
		statements: []string{
			`ALTER TABLE conversations ADD COLUMN workspace_temporary INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		version: 6,
		name:    "inputs（§6 busy input 队列：会话忙时的排队投递 + 审计台账）",
		statements: []string{
			`CREATE TABLE inputs (
				id              TEXT PRIMARY KEY,
				conversation_id TEXT NOT NULL REFERENCES conversations(id),
				text            TEXT NOT NULL DEFAULT '',
				delivery        TEXT NOT NULL DEFAULT 'after_turn',
				target_turn_id  TEXT NOT NULL DEFAULT '',
				generation      INTEGER NOT NULL DEFAULT 0,
				status          TEXT NOT NULL DEFAULT 'queued',
				created_at      TEXT NOT NULL,
				claimed_at      TEXT NOT NULL DEFAULT ''
			)`,
			// 队列读取只按 (会话, 状态) 过滤 + 创建时间升序取队首。
			`CREATE INDEX idx_inputs_queue ON inputs(conversation_id, status, created_at)`,
		},
	},
}

// latestSchemaVersion 当前支持的最新 schema 版本。
func latestSchemaVersion() int {
	return schemaMigrations[len(schemaMigrations)-1].version
}

// Migrate 幂等应用未执行的迁移（自管 schema_migrations：单事务 DDL + 记录版本）。
// 库版本高于本二进制（未知未来版本）→ 拒绝启动（对齐 zacp refuse-start）。
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS ` + model.TableMigration + ` (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`).Error; err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, maxVersion, err := s.migrations.AppliedVersions(ctx)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	if maxVersion > latestSchemaVersion() {
		return fmt.Errorf("store: schema v%d 高于本二进制支持的 v%d（请升级二进制，拒绝启动）", maxVersion, latestSchemaVersion())
	}

	pending := make([]int, 0, len(schemaMigrations))
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
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration 在单个事务内执行一次迁移的语句并记录版本。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, stmt := range m.statements {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("store: apply migration v%d (%s): %w", m.version, m.name, err)
			}
		}
		if err := s.migrations.WithTx(tx).Record(ctx, m.version, m.name); err != nil {
			return fmt.Errorf("store: record migration v%d: %w", m.version, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: migrate v%d: %w", m.version, err)
	}
	return nil
}

// SchemaVersion 返回库当前 schema 版本（0 = 空库/未迁移）。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	_, maxVersion, err := s.migrations.AppliedVersions(ctx)
	if err != nil {
		return 0, err
	}
	return maxVersion, nil
}
