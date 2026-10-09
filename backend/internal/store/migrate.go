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
	{
		version: 7,
		name:    "conversations.origin/capability_json/capability_fingerprint（M3 代际绑定：来源轴 + 钉住的能力明细与指纹）",
		statements: []string{
			// '' = 人工创建；'automation:<id>' = 某条 automation 触发产生（M4 用）。
			// 会话列表逐行渲染"这条是不是自动跑出来的"不能靠回放事件（O(1) 要求），故单列一列。
			`ALTER TABLE conversations ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
			// 创建时解析出的能力明细（选定集，JSON；'' = 未声明 → 回落 agent 白名单）。
			`ALTER TABLE conversations ADD COLUMN capability_json TEXT NOT NULL DEFAULT ''`,
			// **钉住的代际**：会话恢复/续聊要回到同一套能力。指纹单独成列是因为代际回收
			// （M3-7）要按它查"还有哪些会话在用这个代际"（O(log n)，走索引）；塞进 JSON 就得
			// 全表扫 + 逐行解析（O(n)）。'' = 未声明能力选择（与今天行为一致）。
			`ALTER TABLE conversations ADD COLUMN capability_fingerprint TEXT NOT NULL DEFAULT ''`,
			// "某 automation 最近 N 次**产生了会话**的运行"：WHERE origin = ? ORDER BY updated_at DESC。
			`CREATE INDEX idx_conversations_origin ON conversations(origin, updated_at)`,
			// 代际回收：WHERE capability_fingerprint = ?（含空指纹，空值也走索引）。
			`CREATE INDEX idx_conversations_fingerprint ON conversations(capability_fingerprint)`,
		},
	},
	{
		version: 8,
		name:    "skills/mcp_servers/capability_revisions（M6-1 能力事实源：定义行 + 单调变更审计）",
		statements: []string{
			// 定义行：`body` 是唯一权威（原始 JSON，含未知键），只加两个提升列。
			// 刻意不加 enabled/scope/runtimes 等派生列——它们都在 body 里，双写必然漂移
			// （计划 §1 P-2）。定义规模是几十条，查询在 Go 侧做。
			`CREATE TABLE skills (
				id         TEXT PRIMARY KEY,
				body       TEXT NOT NULL,
				revision   INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL
			)`,
			// MCP server 定义与 skills 同形；分表而不是加 kind 列：两边的读取面/写面
			// 与规模各走各的，合并只会让每个查询都带一个恒真的过滤条件。
			`CREATE TABLE mcp_servers (
				id         TEXT PRIMARY KEY,
				body       TEXT NOT NULL,
				revision   INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL
			)`,
			// 变更审计：**刻意不加外键**——审计必须在能力被删除后仍然存在，与 events 表
			// "必须挂会话"的约束正好相反。AUTOINCREMENT 保证 seq 单调不复用，于是它
			// 可以直接当"当前代际 rev"（M1 落地时取 MAX(seq)，不必另造进程内计数器）。
			`CREATE TABLE capability_revisions (
				seq           INTEGER PRIMARY KEY AUTOINCREMENT,
				kind          TEXT NOT NULL,
				capability_id TEXT NOT NULL,
				action        TEXT NOT NULL,
				actor         TEXT NOT NULL,
				at            TEXT NOT NULL,
				digest        TEXT NOT NULL
			)`,
			// "这条能力的变更历史"：WHERE kind = ? AND capability_id = ? ORDER BY seq。
			// seq 先进索引列之外还要带上它，才能同时满足过滤与有序取用。
			`CREATE INDEX idx_capability_revisions_capability ON capability_revisions(kind, capability_id, seq)`,
		},
	},
	{
		version: 9,
		name:    "agents（M6-6 agent 定义事实源；种子仍在代码里，本表只存覆盖）",
		statements: []string{
			// 与 skills/mcp_servers 同形（body 唯一权威 + revision CAS + updated_at）。
			// 变更审计复用 v8 的 capability_revisions（kind='agent'）——"定义变更审计"只有一份。
			//
			// 「当前 Agent」（原 `<AgentDir>/default.json`）不在这里：它是目录级配置，
			// 落 meta（model.MetaKeyDefaultAgent，计划 §7 P-8）。
			`CREATE TABLE agents (
				id         TEXT PRIMARY KEY,
				body       TEXT NOT NULL,
				revision   INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL
			)`,
		},
	},
	{
		version: 10,
		name:    "automations（M6-7 定时任务：定义列 + 运行态列，Plan §7 P-10/P-11）",
		statements: []string{
			// 定义（body + revision/updated_at）与运行态（last_* / consecutive_failures /
			// recent_runs）**分列**：写面只 merge body，于是"往 body 里塞 last_fired_at"这种事
			// 结构上不可能（拒写名单仍然保留，为的是不出现"我写了它为什么不生效"的困惑）。
			//
			// 运行态列不参与 revision CAS：调度器每次推进不该让定义看起来"被别人改过"。
			// 非空约束配默认值：SQLite 的 ALTER 加列才需要默认值，这里新建表也一并写上，
			// 让"未触发过"的落库形态唯一（空串 / 0 / '[]'），不给 NULL 留第二种表示。
			`CREATE TABLE automations (
				id                   TEXT PRIMARY KEY,
				body                 TEXT NOT NULL,
				revision             INTEGER NOT NULL DEFAULT 1,
				updated_at           TEXT NOT NULL,
				last_fired_at        TEXT NOT NULL DEFAULT '',
				last_status          TEXT NOT NULL DEFAULT '',
				last_reason          TEXT NOT NULL DEFAULT '',
				last_conversation_id TEXT NOT NULL DEFAULT '',
				consecutive_failures INTEGER NOT NULL DEFAULT 0,
				recent_runs          TEXT NOT NULL DEFAULT '[]'
			)`,
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
