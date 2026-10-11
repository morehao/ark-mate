package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// migration 是一次原子 schema 变更：其 statements 与 apply 在单个事务内顺序执行。
type migration struct {
	version    int
	name       string
	statements []string
	// apply 是同事务内的 **Go 数据迁移**步骤（在 statements 之后执行）。
	//
	// 为什么需要它：v1–v11 都是纯 DDL，一条语句一行读得完；而 v12 要把 `models` 的身份列
	// 从三元组归并成两元组、重写 `providers.body`、把逐 runtime 的 `is_default` 换算成单一
	// `default_model`，并在同一事务里**逐组对账**。这些表达不了成 SQL 字符串——硬写成 SQL
	// 会把"归并规则"藏进一段没人能审的串里，而它是本次唯一不可逆的数据变换。
	//
	// 失败即整体回滚（事务由 applyMigration 持有），不存在"迁移了一半"。
	apply func(ctx context.Context, tx *gorm.DB, s *Store) error
	// backup 非空 = 执行本迁移**之前**先把库文件复制成 `<db>.bak-<backup>-<ts>`。
	//
	// 只有"就地改写既有数据"的迁移需要它（v12），纯 DDL 不需要。备份只管 DB 文件：
	// vault（密文与主密钥）不在其内，而 v12 也不触碰 vault，所以回退时不必恢复 vault
	// （§安全：完整备份是三件套 mate.db + vault/ + vault.key）。
	backup string
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
	{
		version: 11,
		name:    "providers/models（设置 · 模型：供应商凭据迁 DB + 平台模型清单）",
		statements: []string{
			// provider 档案：与 skills/mcp_servers/agents 同形（body 唯一权威 + revision CAS +
			// updated_at）。明文密钥绝不进 body（只允许 key_ref），密文仍在 vault。
			// 同版建 models，是为了不出现「升级后供应商有了、模型还不能用」的中间态
			// （设计「数据迁移与校验」第 2 条）。
			`CREATE TABLE providers (
				id         TEXT PRIMARY KEY,
				body       TEXT NOT NULL,
				revision   INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL
			)`,
			// 平台模型清单：比 providers 多两个**身份列**（runtime / provider_id）——
			// 它们是平台侧唯一的查询/约束轴（物化按 runtime 取；删除保护按 provider_id 查），
			// 都在热路径上，放 body 就得全表扫 + 反序列化。
			//
			// is_default 是列而不是 body 字段：每 runtime 默认的唯一性由「先清同 runtime 再置新」
			// 两条 SQL 在同一事务里保证，且默认模型的取值面是 `WHERE runtime=? AND is_default=1`。
			// 落库形态是 INTEGER 0/1（model.FlagValue；裸 SQL 不经过 serializer）。
			`CREATE TABLE models (
				id          TEXT PRIMARY KEY,
				runtime     TEXT NOT NULL,
				provider_id TEXT NOT NULL,
				body        TEXT NOT NULL,
				is_default  INTEGER NOT NULL DEFAULT 0,
				revision    INTEGER NOT NULL DEFAULT 1,
				updated_at  TEXT NOT NULL
			)`,
			// 物化按 runtime 取清单（热路径）；删除保护按 provider_id 查引用（热路径）。
			`CREATE INDEX idx_models_runtime ON models(runtime)`,
			`CREATE INDEX idx_models_provider ON models(provider_id)`,
		},
	},
	{
		version: 12,
		name:    "providers/models 去底座维度（v12）：body 改写 + (provider_id,model_id) 归并 + meta.default_model",
		backup:  "v12",
		statements: []string{
			// 归并的目标表：`model_id` 从 body 提升为**列**，`is_default` 删除（默认模型改住
			// meta 的单一值）。先建成临时名，Go 步骤把归并结果灌进去，最后 DROP 旧表 + RENAME
			// ——整套在同一事务里，失败即回到 v11 的样子。
			`CREATE TABLE models_v12 (
				id          TEXT PRIMARY KEY,
				provider_id TEXT NOT NULL,
				model_id    TEXT NOT NULL,
				body        TEXT NOT NULL,
				revision    INTEGER NOT NULL DEFAULT 1,
				updated_at  TEXT NOT NULL
			)`,
		},
		apply: migrateV12,
	},
	{
		version: 13,
		name:    "providers/models 列化（v13）：平台字段提列 + 删 body/key_ref + input_modalities",
		backup:  "v13",
		statements: []string{
			// 目标表：两表的**平台字段全部成列**，`body` 与 `key_ref` 消失（ADR-1/ADR-7）。
			// 先建成临时名，Go 步骤把 v12 的 body 解析成列灌进去，最后 DROP 旧表 + RENAME
			// ——整套在同一事务里，失败即回到 v12 的样子（与 migrate_v12 同一手法）。
			//
			// `CHECK (protocol IN ('openai'))` 是库级约束（ADR-3）：协议白名单是**安全相关**的
			// （它决定把密钥发到哪种形态的端点），只靠 Go 侧一处校验时，裸 SQL / 导入 / 将来
			// 迁移都能绕过去。本期只有一种取值，将来加协议本来就必须有一次表重建迁移。
			`CREATE TABLE providers_v13 (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL DEFAULT '',
				protocol   TEXT NOT NULL CHECK (protocol IN ('openai')),
				base_url   TEXT NOT NULL DEFAULT '',
				note       TEXT NOT NULL DEFAULT '',
				revision   INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			// `input_modalities` 两态（`text` | `text,image`）**默认 `text`**：平台总是写
			// 底座声明里的 `models[].input`（ADR-12），所以"默认 text"是**明确值**而不是"没设"。
			//
			// 唯一约束刻意**不在建表语句里**而在换表后单独建索引：写成表内 UNIQUE 会留下
			// 自动索引名（`sqlite_autoindex_*`），换名之后它与表名的对应关系没人看得懂。
			`CREATE TABLE models_v13 (
				id               TEXT PRIMARY KEY,
				provider_id      TEXT NOT NULL,
				model_id         TEXT NOT NULL,
				name             TEXT NOT NULL DEFAULT '',
				note             TEXT NOT NULL DEFAULT '',
				input_modalities TEXT NOT NULL DEFAULT 'text',
				revision         INTEGER NOT NULL DEFAULT 1,
				created_at       TEXT NOT NULL,
				updated_at       TEXT NOT NULL
			)`,
		},
		apply: migrateV13,
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
		// 备份必须发生在**事务开始之前**——它要复制的是"迁移前那个文件"，
		// 而 SQLite 的事务是文件级的，事务内复制拿到的是未提交视图，回退时不可靠。
		if m.backup != "" {
			path, berr := s.backupBeforeMigration(m)
			if berr != nil {
				return berr
			}
			s.migrationBackupPath = path
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// backupBeforeMigration 把库文件复制成 `<db>.bak-<tag>-<ts>`，返回该路径。
//
// 就地改写既有数据的迁移（v12）在用户库上是**不可逆点**：一旦 v12 落地，旧二进制会因为
// `latestSchemaVersion` 前进而拒绝启动，唯一的退路就是这份文件（§回退窗口）。
// 所以它必须是"复制成功才算数"：复制失败 → **不迁移**（留在旧版本上，如实报错），
// 而不是"先迁了再说"。
//
// 内存库（path 为空）不备份：没有文件可复制，而它也不承载用户数据（测试/演示形态）。
func (s *Store) backupBeforeMigration(m migration) (string, error) {
	if s.path == "" || s.path == ":memory:" {
		return "", nil
	}
	src, err := os.Open(s.path)
	if err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 前无法打开库文件备份（拒绝迁移）: %w", m.version, err)
	}
	defer src.Close()
	dst := fmt.Sprintf("%s.bak-%s-%s", s.path, m.backup, time.Now().UTC().Format("20060102T150405Z"))
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 前无法创建备份 %s（拒绝迁移）: %w", m.version, dst, err)
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("store: 迁移 v%d 的备份写入失败（拒绝迁移）: %w", m.version, err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 的备份收尾失败（拒绝迁移）: %w", m.version, err)
	}
	return dst, nil
}

// MigrationBackupPath 返回本次进程启动时那次迁移备份的路径（空 = 没有迁移过，或库无文件）。
//
// `mate doctor` 用它打印"回退到旧版本要恢复哪个文件"（§回退窗口的对外口径）。
func (s *Store) MigrationBackupPath() string { return s.migrationBackupPath }

// MigrationBackupPaths 列出本机全部 DB 迁移备份（新的在前；空 = 没迁移过或库无文件）。
//
// `mate doctor` 用它打印"回退到旧版本要恢复哪个文件"——体检常常跑在**另一次启动**里
// （迁移已经发生过了，本次进程没有备份记录），所以它必须能从磁盘上找出来。
//
// **按版本扫描而不是钉死一个 tag**（v12 时这里硬编码 `"v12"`）：库每前进一版，回退目标
// 就换一个版本，钉死会让 doctor 在 v13 之后仍然只报 v12 的备份——而那份备份恢复回去
// 得到的正是 v12，用户以为找到了退路，实际是退了一版后再被拒启。新的在前（`v13` > `v12`
// 字典序成立，同版本内时间戳大的在前）。
func (s *Store) MigrationBackupPaths() []string { return s.findMigrationBackups("") }

// findMigrationBackups 列出库文件所在目录里 `<db>.bak-<tag>-*` 形式的备份（新的在前）。
//
// 用 glob 而不是只认内存里那一份：doctor 常常在**另一次**启动里跑（迁移已经发生过了，
// 本次进程没有备份记录），而用户问的正是"我该恢复哪个文件"。
//
// `tag == ""` = 扫全部版本（`.bak-v*`）：调用方（doctor）要的是"这台机器上所有可用的退路"，
// 而不是某一个版本。非空则只认那一个 tag。
func (s *Store) findMigrationBackups(tag string) []string {
	if s.path == "" || s.path == ":memory:" {
		return nil
	}
	pattern := s.path + ".bak-v*"
	if tag != "" {
		pattern = s.path + ".bak-" + tag + "-*"
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

// applyMigration 在单个事务内执行一次迁移的语句与数据步骤，并记录版本。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, stmt := range m.statements {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("store: apply migration v%d (%s): %w", m.version, m.name, err)
			}
		}
		if m.apply != nil {
			if err := m.apply(ctx, tx, s); err != nil {
				return fmt.Errorf("store: apply migration v%d (%s) 的数据步骤: %w", m.version, m.name, err)
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
