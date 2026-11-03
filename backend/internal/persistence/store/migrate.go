package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
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
	// backup 非空 = 执行本迁移**之前**先把库文件复制成
	// `<db 目录>/backups/<db 文件名>.bak-<backup>-<ts>`（v19 起的布局；此前落在库文件同级）。
	//
	// 只有"就地改写既有数据"的迁移需要它（v12），纯 DDL 不需要。备份只管 DB 文件：
	// v20 起 `providers.key` 进库，所以这份备份与 `mate.db` 同级敏感（备份目录 0700、文件 0600）。
	//
	// 历史迁移注里写的 `<db>.bak-v15-*` 一类是**文件名**形状（目录从 v19 起是 `backups/`），
	// doctor 打印的是完整路径；老布局的备份仍被扫描与列出（见 MigrationBackupPathsLegacy）。
	backup string
}

// schemaMigrations 是版本-迁移表（有序；追加迁移 = 在尾部新增，绝不回改历史）。
// v1 baseline：meta / conversations / events / runs / messages / permission_memories + 索引。
//
// 这里刻意不用 gorm 的 AutoMigrate：AutoMigrate 只做「往目标结构收敛」，
// 表达不了本仓要求的三件事——严格有序的版本号、库版本高于二进制时**拒绝启动**、
// 以及导入路径的 staging 回滚。列定义因此仍与 internal/persistence/model 的实体手工保持一致
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
			// 落 meta 的 `default_agent` 键（**该键与这张表都在 v19 一并删除**，见 v19 那一步）。
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
	{
		version: 14,
		name:    "工作空间登记表（v14）：workspaces + conversations.workspace_id",
		backup:  "v14",
		statements: []string{
			// 登记项：一个规范化目录一行（`path` 唯一索引是"一个目录一个工作空间"的唯一执行者，
			// 设计 ADR-1）。**登记表里刻意没有成员账**（`session_ids` 之类）：成员关系由
			// `conversations.workspace_id` 表达，见 model.Workspace 的注释。
			`CREATE TABLE workspaces (
				id         TEXT PRIMARY KEY,
				path       TEXT NOT NULL,
				title      TEXT NOT NULL DEFAULT '',
				is_default INTEGER NOT NULL DEFAULT 0,
				list_order INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX idx_workspaces_path ON workspaces(path)`,
			// 「有且只有一个默认工作空间」由**部分唯一索引**保证：普通唯一索引会把几十行
			// `is_default = 0` 判成重复，而这条不变量恰恰只约束取值为 1 的那一行。
			`CREATE UNIQUE INDEX idx_workspaces_default ON workspaces(is_default) WHERE is_default = 1`,
			`CREATE INDEX idx_workspaces_order ON workspaces(list_order)`,
			// 归属列：`DEFAULT ''` 让历史行**天然**落成"归默认工作空间"（读面解析），
			// 于是不需要一次全表 UPDATE——少一条全表写就少一次中断面（见 migrate_v14.go）。
			//
			// **故意没有 REFERENCES**：删工作空间登记**不该**连带删会话（设计 ADR-6），
			// 级联恰恰是错的语义；归属的解析放在读面（空串 = 默认工作空间）。
			// 这条在 v1.32.22 之后是**承重的**：那时候起外键真的生效（ncruces 的 wasm 构建带
			// SQLITE_DEFAULT_FOREIGN_KEYS=1），补一个 REFERENCES 进来就会真的咬人——
			// 而它当年（mattn 方言，foreign_keys=0）只是装饰。
			`ALTER TABLE conversations ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`,
			// 左栏分组要按 (归档, 归属, 时间) 取数，索引照这个顺序建。
			`CREATE INDEX idx_conversations_workspace ON conversations(archived, workspace_id, updated_at)`,
		},
		apply: migrateV14,
	},
	{
		version: 15,
		name:    "破坏性重建（仅此一次，无行级迁移）：会话词汇统一 + 主键前缀化",
		// 备份是这次唯一的回退窗口：7 张表的行会被清空（会话/事件/轮次/消息/排队输入/
		// 权限记忆/工作空间登记），`mate doctor` 会打印 `<db>.bak-v15-*`。
		backup: "v15",
		// 语句**全是字面量**：表名在此处是历史命名，绝不引用 model.TableName* 常量
		// （否则实体改名会把这条历史步骤一起改掉——守卫⑤ 钉着这条纪律）。
		//
		// `models` **刻意不在这张清单里**（2026-10-11 用户拍板）：它只换主键形态、列集合
		// 不变，所以在 apply 里把旧行 id 重铸成 `mdl_<ULID>` 并同事务改指
		// `meta.default_model`（R7）。保持表不动的直接好处：两个索引与全体模型行的内容
		// 都不用重建。
		statements: []string{
			`DROP TABLE IF EXISTS conversations`,
			`DROP TABLE IF EXISTS events`,
			`DROP TABLE IF EXISTS runs`,
			`DROP TABLE IF EXISTS messages`,
			`DROP TABLE IF EXISTS permission_memories`,
			`DROP TABLE IF EXISTS inputs`,
			`DROP TABLE IF EXISTS workspaces`,

			// `sessions` = v14 的 conversations 列集合（v2/v4/v5/v7/v14 加过的列全在），
			// 只换表名与主键形态。
			`CREATE TABLE sessions (
				id                     TEXT PRIMARY KEY,
				agent_id               TEXT NOT NULL,
				cwd                    TEXT NOT NULL DEFAULT '',
				workspace_id           TEXT NOT NULL DEFAULT '',
				workspace_temporary    INTEGER NOT NULL DEFAULT 0,
				provider               TEXT NOT NULL DEFAULT '',
				model                  TEXT NOT NULL DEFAULT '',
				runtime_hint           TEXT NOT NULL DEFAULT '',
				provider_session_id    TEXT NOT NULL DEFAULT '',
				title                  TEXT NOT NULL DEFAULT '',
				pinned                 INTEGER NOT NULL DEFAULT 0,
				archived               INTEGER NOT NULL DEFAULT 0,
				status                 TEXT NOT NULL DEFAULT 'in_progress',
				origin                 TEXT NOT NULL DEFAULT '',
				capability_json        TEXT NOT NULL DEFAULT '',
				capability_fingerprint TEXT NOT NULL DEFAULT '',
				last_seq               INTEGER NOT NULL DEFAULT 0,
				created_at             TEXT NOT NULL,
				updated_at             TEXT NOT NULL
			)`,
			`CREATE INDEX idx_sessions_list ON sessions(archived, updated_at)`,
			`CREATE INDEX idx_sessions_origin ON sessions(origin, updated_at)`,
			`CREATE INDEX idx_sessions_fingerprint ON sessions(capability_fingerprint)`,
			`CREATE INDEX idx_sessions_workspace ON sessions(archived, workspace_id, updated_at)`,

			// events 的身份仍是 seq（历史回放分页用的就是它），**刻意不加 id 列**。
			// 〔v17 推翻了这一条：events 的主键换成平台铸造的 `evt_…`，`seq` 降为唯一游标——
			// 见本文件 version 17 与 `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`。
			// 这里保持当时的语句与理由不动（迁移纪律：历史步骤不回改）。〕
			`CREATE TABLE events (
				seq          INTEGER PRIMARY KEY,
				session_id   TEXT NOT NULL REFERENCES sessions(id),
				type         TEXT NOT NULL,
				payload_json TEXT NOT NULL DEFAULT '',
				created_at   TEXT NOT NULL
			)`,
			`CREATE INDEX idx_events_session ON events(session_id, seq)`,

			`CREATE TABLE runs (
				id             TEXT PRIMARY KEY,
				session_id     TEXT NOT NULL REFERENCES sessions(id),
				user_text      TEXT NOT NULL DEFAULT '',
				status         TEXT NOT NULL DEFAULT 'running',
				error_code     TEXT NOT NULL DEFAULT '',
				stop_reason    TEXT NOT NULL DEFAULT '',
				usage_json     TEXT NOT NULL DEFAULT '',
				first_seq      INTEGER NOT NULL DEFAULT 0,
				last_seq       INTEGER NOT NULL DEFAULT 0,
				first_token_ms INTEGER NOT NULL DEFAULT 0,
				started_at     TEXT NOT NULL,
				ended_at       TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX idx_runs_session ON runs(session_id, started_at)`,

			// `messages.id` 从 `INTEGER PRIMARY KEY AUTOINCREMENT` 换成 `TEXT PRIMARY KEY`
			// （SQLite 不能 ALTER 主键类型，所以只能随这次重建一起换）。ULID 的字典序 =
			// 铸造序，于是 `ORDER BY seq_lo ASC, id ASC` 的排序语义逐字不变（P3）。
			`CREATE TABLE messages (
				id                TEXT PRIMARY KEY,
				session_id        TEXT NOT NULL REFERENCES sessions(id),
				run_id            TEXT NOT NULL DEFAULT '',
				role              TEXT NOT NULL,
				text              TEXT NOT NULL DEFAULT '',
				events_json       TEXT NOT NULL DEFAULT '',
				tool_details_json TEXT NOT NULL DEFAULT '',
				seq_lo            INTEGER NOT NULL DEFAULT 0,
				seq_hi            INTEGER NOT NULL DEFAULT 0,
				created_at        TEXT NOT NULL
			)`,
			`CREATE INDEX idx_messages_session ON messages(session_id, run_id)`,

			// 真正的键是 `UNIQUE(session_id, title, kind)`；`id` 列保留只为 gormdao 的主键
			// 泛型方法（登记为 D-1 技术债）。
			`CREATE TABLE permission_memories (
				id         TEXT PRIMARY KEY,
				session_id TEXT NOT NULL REFERENCES sessions(id),
				title      TEXT NOT NULL,
				kind       TEXT NOT NULL DEFAULT '',
				decision   TEXT NOT NULL,
				created_at TEXT NOT NULL,
				UNIQUE(session_id, title, kind)
			)`,
			`CREATE INDEX idx_memories_session ON permission_memories(session_id)`,

			`CREATE TABLE inputs (
				id              TEXT PRIMARY KEY,
				session_id      TEXT NOT NULL REFERENCES sessions(id),
				text            TEXT NOT NULL DEFAULT '',
				delivery        TEXT NOT NULL DEFAULT 'after_turn',
				target_turn_id  TEXT NOT NULL DEFAULT '',
				generation      INTEGER NOT NULL DEFAULT 0,
				status          TEXT NOT NULL DEFAULT 'queued',
				created_at      TEXT NOT NULL,
				claimed_at      TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX idx_inputs_queue ON inputs(session_id, status, created_at)`,

			`CREATE TABLE workspaces (
				id         TEXT PRIMARY KEY,
				path       TEXT NOT NULL,
				title      TEXT NOT NULL DEFAULT '',
				is_default INTEGER NOT NULL DEFAULT 0,
				list_order INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX idx_workspaces_path ON workspaces(path)`,
			`CREATE UNIQUE INDEX idx_workspaces_default ON workspaces(is_default) WHERE is_default = 1`,
			`CREATE INDEX idx_workspaces_order ON workspaces(list_order)`,

			// `automations` 本次**不重建**（它有文件来源，且运行态里的 `last_fired_at` 必须
			// 保留，ADR-10），但它的运行态列名带着旧词汇：`last_conversation_id` 指向的是
			// **平台会话 id**，所以随词汇统一改名。列改名而不是重建表，是为了不动
			// `last_fired_at` 与全体定义行。
			`ALTER TABLE automations RENAME COLUMN last_conversation_id TO last_session_id`,
		},
		apply: migrateV15,
	},
	{
		version: 16,
		name:    "命名收口：定义变更审计改名 + 轮次词汇统一到 run（一张表重建，其余纯改列/改索引）",
		// 备份：本步**重建 `inputs`**（把 `delivery='after_turn'` 的存货行改写成
		// `'after_run'`）——它是"就地改写既有数据"，与 v12/v15 同一档。旧二进制对 v16 库是
		// **拒绝启动**，所以备份是唯一的回退窗口，`mate doctor` 会打印 `<db>.bak-v16-*`。
		//
		// 与 v15 的区别：v15 清空了 7 张表的行，本步**一行不丢**（`INSERT INTO … SELECT`
		// 把行搬过去，失败即整体回滚）。备份在这里买的是"改错了能退"，不是"数据本来就要没了"。
		backup: "v16",
		// 三条命名收口（设计 `.dsh/docs/specs/2026-10-11-data-model-v16-design.md`）：
		//   1. `capability_revisions` → `definition_revisions`，`capability_id` → `definition_id`；
		//   2. `turn` → `run`（`inputs.target_turn_id` → `target_run_id`、
		//      `inputs.delivery` 的 `after_turn` → `after_run`）；
		//   3. 索引名一律 `idx_<表名>_<用途>`（`idx_memories_session` 的表名段漏了）。
		//
		// **语句全是字面量**（与 v15 同一条纪律）：这里出现的 `capability_revisions`、
		// `capability_id`、`target_turn_id`、`after_turn`、`idx_memories_session` 都是
		// **被改掉的那一版的名字**，绝不引用 `model.TableName*` 常量或 `model.DefinitionKind*`
		// ——引用常量会让"实体改名"把这条历史步骤一起改掉，于是老库（真的是旧列名）与
		// 新库（被改成了新列名）走出两条不同的路（守卫⑤/⑥ 钉着这条纪律）。
		statements: []string{
			// ——— 1. 定义变更审计 ———
			//
			// SQLite 的 `ALTER TABLE … RENAME TO` **不会顺手改索引名**（索引名与表名是两个
			// 独立的名字空间条目），所以下面必须显式 DROP 旧索引 / CREATE 新索引；而
			// `RENAME COLUMN` **会**改写引用该列的索引定义，所以先改名再重建索引。
			`ALTER TABLE capability_revisions RENAME TO definition_revisions`,
			`ALTER TABLE definition_revisions RENAME COLUMN capability_id TO definition_id`,
			`DROP INDEX idx_capability_revisions_capability`,
			`CREATE INDEX idx_definition_revisions_history ON definition_revisions(kind, definition_id, seq)`,

			// ——— 2. 会话级模型选择的两个引用列 ———
			//
			// 库里这两列是**引用**（`providers.id` / `models.model_id`），所以按"引用列 `_id` 结尾"
			// 改名；wire（DTO/前端）上的 `provider` / `model` 是**选择器**，保持原名（设计 D5）。
			`ALTER TABLE sessions RENAME COLUMN provider TO provider_id`,
			`ALTER TABLE sessions RENAME COLUMN model TO model_id`,

			// ——— 3. inputs：列名与取值一起改，只能重建 ———
			//
			// `delivery` 的**列默认值**也是 `'after_turn'`，而 SQLite 改不了列默认值
			// （没有 `ALTER COLUMN`），`RENAME COLUMN` 又只解决列名。留着默认值不改是
			// 最坏的一种收口：schema 文本里继续写着 `after_turn`，下一个读 schema 的人
			// 会以为这个取值还活着。故整表重建（行照搬，`delivery` 就值改写）。
			`CREATE TABLE inputs_v16 (
				id            TEXT PRIMARY KEY,
				session_id    TEXT NOT NULL REFERENCES sessions(id),
				text          TEXT NOT NULL DEFAULT '',
				delivery      TEXT NOT NULL DEFAULT 'after_run',
				target_run_id TEXT NOT NULL DEFAULT '',
				generation    INTEGER NOT NULL DEFAULT 0,
				status        TEXT NOT NULL DEFAULT 'queued',
				created_at    TEXT NOT NULL,
				claimed_at    TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO inputs_v16 (id, session_id, text, delivery, target_run_id, generation, status, created_at, claimed_at)
				SELECT id, session_id, text,
				       CASE WHEN delivery = 'after_turn' THEN 'after_run' ELSE delivery END,
				       target_turn_id, generation, status, created_at, claimed_at
				FROM inputs`,
			`DROP TABLE inputs`,
			`ALTER TABLE inputs_v16 RENAME TO inputs`,
			`CREATE INDEX idx_inputs_queue ON inputs(session_id, status, created_at)`,

			// ——— 4. 索引名补上表名段 ———
			//
			// `permission_memories` 的表名段曾被缩写成 `memories`，于是"这条索引属于哪张表"
			// 只能靠人记（`events`/`runs` 早就有 `idx_<表>_session` 了）。判据是
			// `TestIndexNamesFollowTablePrefix`。
			`DROP INDEX idx_memories_session`,
			`CREATE INDEX idx_permission_memories_session ON permission_memories(session_id)`,
		},
	},
	{
		version: 17,
		name:    "事件身份：events 主键换成平台铸造 id（evt_）+ 就地重建；sessions 两列收口到 runtime 词",
		// 备份：本步**就地重建 `events`**（逐行铸 `evt_` 身份，主键从 seq 换成 id）——它是
		// "就地改写既有数据"，与 v12/v13/v16 同一档。旧二进制对 v17 库是**拒绝启动**，所以备份
		// 是唯一的回退窗口，`mate doctor` 会打印 `<db>.bak-v17-*`。
		//
		// 与 v15 相反的一处（很容易照抄错）：v15 重建 events 时**清空**了
		// `meta.events_last_seq`（因为行被清掉了）；v17 **行照搬、一行不丢**，所以那一行 meta
		// 必须原样留着——清掉它会让新事件从 1 重新起跳、与既有行撞号，而 `idx_events_seq` 的
		// UNIQUE 会当场把这次迁移判红。
		backup: "v17",
		// 两条改动（设计 `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`）：
		//   1. 事件的身份与位次分开：`id`（`evt_<26 位 ULID>`，平台铸造）当主键，`seq` 降为
		//      带唯一索引的普通列；transcript 升 v2 让两者一起随数据流动；
		//   2. `sessions.provider_session_id` → `runtime_session_id`、
		//      `sessions.runtime_hint` → `runtime_id`（`provider` 在本仓是"模型供应商"的词）。
		//
		// **语句全是字面量**（与 v15/v16 同一条纪律）：这里出现的 `provider_session_id` /
		// `runtime_hint` 是**被改掉的那一版的名字**，绝不引用 `model.TableName*` 或 Go 字段名
		// ——引用会让"实体改名"把这条历史步骤一起改掉，老库（真的叫旧名）与新库走出两条路
		// （守卫⑤ 钉着这条纪律）。
		statements: []string{
			// 目标表：`id` 主键 + `seq` 普通列。唯一约束刻意**不在建表语句里**（表内 UNIQUE 会
			// 留下 `sqlite_autoindex_*`，换名后没人看得懂它属于谁——与 v13 同一条理由），
			// 换表之后单独 `CREATE UNIQUE INDEX idx_events_seq`。
			`CREATE TABLE events_v17 (
				id           TEXT PRIMARY KEY,
				seq          INTEGER NOT NULL,
				session_id   TEXT NOT NULL REFERENCES sessions(id),
				type         TEXT NOT NULL,
				payload_json TEXT NOT NULL DEFAULT '',
				created_at   TEXT NOT NULL
			)`,

			// ——— 2. 两列收口 ———
			//
			// 纯改名（不搬行）：`RENAME COLUMN` 会改写引用该列的索引定义，而 sessions 的 4 条
			// 索引都不引用这两列，所以本步没有索引要重建。
			`ALTER TABLE sessions RENAME COLUMN provider_session_id TO runtime_session_id`,
			`ALTER TABLE sessions RENAME COLUMN runtime_hint TO runtime_id`,
		},
		apply: migrateV17,
	},
	{
		version: 19,
		name: "agent 概念并入 runtime：DROP agents + 清 default_agent/imported_agents 与 kind='agent' 审计" +
			" + sessions.agent_id 出列",
		// 备份：本步**删列**（`sessions.agent_id`）并就地删掉既有行——它改写了既有数据，与
		// v12/v13/v15/v16/v17 同一档。旧二进制对 v19 库是**拒绝启动**（`latestSchemaVersion`
		// 前进），所以备份是唯一的回退窗口，`mate doctor` 会打印 `<db>.bak-v19-*`。
		//
		// v19 的口径（`docs/decisions.md`「决策 v19」）：agent 与 runtime 是**同一个
		// 东西**（底座由构建期固定、用户不可自定义），所以 agent 这个身份整体取消，会话身份只留
		// `runtime_id`。本步一次到位，**不留兼容层、不要求旧数据可读**。
		backup: "v19",
		// 语句全是**字面量**（与 v15–v17 同一条纪律）：`agents` / `agent_id` / `default_agent` /
		// `imported_agents` / `kind = 'agent'` 都是**当时那一版**的名字，绝不引用
		// `model.TableName*` 常量或 Go 字段名——引用会让实体改名把这条历史步骤一起改掉。
		statements: []string{
			// 1. agent 定义表整体出列：它的行只是"用户对种子的覆盖"，而种子随概念一起消失。
			`DROP TABLE IF EXISTS agents`,

			// 2. 定义变更审计里 `kind = 'agent'` 的行：这个 kind 以后不存在了，留着它会让
			//    `definition_revisions` 里出现一种查不到定义、也没人能处置的孤儿记录。
			`DELETE FROM definition_revisions WHERE kind = 'agent'`,

			// 3. meta 里两个 agent 面键：「当前 Agent」的显式选择与 agent 定义导入标记。
			`DELETE FROM meta WHERE key IN ('default_agent', 'imported_agents')`,

			// 4. `sessions.agent_id` 出列。
			//
			//    用 `DROP COLUMN` 而不是重建表：本仓已实测该列上**没有**索引/约束/生成列/视图
			//    引用它（sessions 的 4 条索引是 list/origin/fingerprint/workspace，都不含它），
			//    而 SQLite 3.35+ 的 DROP COLUMN 会原子地重写表定义——重建表要连那 4 条索引与
			//    别的表里指向 `sessions(id)` 的外键子句一起搬，出错面更大且会动到无关的行。
			`ALTER TABLE sessions DROP COLUMN agent_id`,
		},
	},
	{
		version: 20,
		name:    "凭据去 vault 化：providers.key 进库 + mcp_secrets 表 + MCP 定义体 vault: → store: 前缀重写",
		// 备份：本步**加列 + 建表 + 就地改写既有行**（MCP 定义体的引用前缀）——改写了既有数据，
		// 与 v12/v13/v15/v16/v17/v19 同一档。旧二进制对 v20 库**拒绝启动**（`latestSchemaVersion`
		// 前进），所以备份是唯一的回退窗口。
		backup: "v20",
		// 语句全是**字面量**（同一条纪律）：`key` / `mcp_secrets` / `vault:` 都是**当时那一版**
		// 的名字，绝不引用 `model` 常量或 Go 字段名。
		statements: []string{
			// 1. 供应商明文密钥进库（空串 = 没有密钥；"有没有"从此只剩 `key != ''` 一个判据）。
			//
			//    用 ALTER 而不是重建表：v13 那张表的列集合是**封闭**的（A1 判据读的就是它的
			//    建表语句），重建会把"当时那一版"的列集合改写掉，让那条历史判据失去参照。
			`ALTER TABLE providers ADD COLUMN key TEXT NOT NULL DEFAULT ''`,

			// 2. MCP 密钥独立成表（一列装不下多个引用，且绝不能进 `mcp_servers.body`——body 会进
			//    `definition_revisions` 与导入导出载体）。键是**引用串本身**：同一个引用可以被多个
			//    server 共用，"谁引用了它"由定义体决定（与旧 vault 的引用语义逐字相同，纯搬运）。
			`CREATE TABLE mcp_secrets (
				ref        TEXT PRIMARY KEY,
				value      TEXT NOT NULL DEFAULT '',
				revision   INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL
			)`,

			// 3. 引用词表收口：`vault:` → `store:`（只改前缀，值一个字不动）。一次性就地改写既有
			//    定义体——留着旧前缀等于在库里留一个指向已删机制的引用，而它的失效方式是**静默**的
			//    （定义照常读出来，只是运行时再也解析不到那把钥匙）。
			`UPDATE mcp_servers SET body = replace(body, 'vault:', 'store:')`,
		},
	},

	// v21 的版本步本体在 `migrate_v21.go`（它自带一段必须与人讲清楚的**步骤顺序理由**：
	// 读模型窗口列的重编号必须发生在 `events` 重编号**之前**，否则旧位次消失、映射失去参照）。
	migrationV21,

	// v22 的版本步本体在 `migrate_v22.go`（自带一段 Go 数据迁移：`runs.id` 从派生串重铸成
	// `run_<ULID>`，并在同一事务里改指 `messages.run_id`——主键重铸 ⇒ 引用必须一起改）。
	migrationV22,

	// v23 的版本步本体在 `migrate_v23.go`（`messages` 补会话内序号 `ordinal` 与种类 `kind`，
	// 并把索引按"整会话读"重排——序号是"投影完不完整"这条不变式的载体）。
	migrationV23,

	// v24 的版本步本体在 `migrate_v24.go`（**纯改名**：`models.model_id` → `models.model`；
	// 本步零行为变化，验收判据是 `git diff testdata/` 为空——理由写在该文件头）。
	migrationV24,

	// v25 的版本步本体在 `migrate_v25.go`（`models` 补两个**可空、无 DEFAULT** 的容量列——
	// 路由键隔离之后容量不再能从底座目录继承，只能由平台声明；理由写在该文件头）。
	migrationV25,

	// v26 的版本步本体在 `migrate_v26.go`（会话模型引用改**平台行 id**：删掉 v19 之后就没有写入点
	// 的 `sessions.provider_id`，并给存量 `model_id` 置一个"待回填"标记——回填在应用层做，因为
	// store 不得 import adapter；理由写在该文件头）。
	migrationV26,

	// v27 的版本步本体在 `migrate_v27.go`（工作空间补两个**默认值**列；本步**一个字都不改
	// `sessions`**——默认值只影响以后建的会话，理由写在该文件头。`permission_memories` 的
	// `scope` 重建挪到 v28 / P2-a：一列加上用它的写面属于同一件事）。
	migrationV27,

	// v28 的版本步本体在 `migrate_v28.go`（权限记忆带上**授权形态**：`scope` + 授权时那条工具
	// 入参的摘要。**唯一键刻意不变**——把入参并进键会把"这类工具始终允许"拆成"这一个参数组合
	// 始终允许"；理由写在该文件头）。
	migrationV28,

	// v29 的版本步本体在 `migrate_v29.go`（会话**钉住**自己的原生权限模式：模式只能以进程 env
	// 下发（`/permission` 命令面不存在），所以"一个进程内按会话切换"走不通，只能每模式一个暖
	// 进程；而暖进程的键要在**取连接之前**已知 ⇒ 模式必须落库。语义见该文件头）。
	migrationV29,

	// v30 的版本步本体在 `migrate_v30.go`（数据归一：`runs.status` 的历史 `running` → 写入方
	// 实际用的 `in_progress`。两个值并存让 `RunningRuns` 恒空、用量 `Running` 恒 0，而这两件事
	// 都不报错——理由写在该文件头）。
	migrationV30,

	// v31 的版本步本体在 `migrate_v31.go`（新增 `experts` 专家定义表：会话级能力预设的
	// body+revision 载体，与 skills/mcp_servers 同形；纯 DDL、无回填——理由写在该文件头）。
	migrationV31,

	// v32 的版本步本体在 `migrate_v32.go`（新增 `attachments` 附件业务索引表：会话归属 +
	// 投递形态 + pending/linked/orphan 生命周期。**字节落盘与 core_file* 两表由 golib filestore
	// 负责、不在本步**——理由写在该文件头）。
	migrationV32,

	// v33 的版本步本体在 `migrate_v33.go`（新增资料库目录树三表：`library_nodes` /
	// `library_attachments` / `library_tables`。**字节落盘复用附件链路 filestore/attachments、
	// 本步不建 blob 表**；本期只做资料库、不做知识库，故**无检索索引表**——理由写在该文件头）。
	migrationV33,

	// v34 的版本步本体在 `migrate_v34.go`（新增产物表 `artifacts` + `library_nodes` 加
	// `category`/`provenance_id` 两列。产物不存字节，字节复用 filestore——理由写在该文件头）。
	migrationV34,
}

// latestSchemaVersion 当前支持的最新 schema 版本。
func latestSchemaVersion() int {
	return schemaMigrations[len(schemaMigrations)-1].version
}

// appliedMigrationVersions 返回已应用的版本集合与最大版本（空库 → 空集合 + 0）。
//
// 读序（version ASC）与"最大版本"的口径都属于迁移内核，所以留在 store；DAO 只负责记账。
func (s *Store) appliedMigrationVersions(ctx context.Context) (map[int]bool, int, error) {
	list, err := s.migrations.GetListByCond(ctx, &gormdao.BaseCond{OrderField: "version ASC"})
	if err != nil {
		return nil, 0, err
	}
	applied := make(map[int]bool, len(list))
	maxVersion := 0
	for _, m := range list {
		applied[m.Version] = true
		if m.Version > maxVersion {
			maxVersion = m.Version
		}
	}
	return applied, maxVersion, nil
}

// Migrate 幂等应用未执行的迁移（自管 schema_migrations：单事务 DDL + 记录版本）。
// 库版本高于本二进制（未知未来版本）→ 拒绝启动（对齐 zacp refuse-start）。
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS ` + model.TableNameSchemaMigration + ` (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`).Error; err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, maxVersion, err := s.appliedMigrationVersions(ctx)
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
		// v15 是唯一一次"清库重建"（守卫⑥ 钉着这条）。只有**升级既有库**才值得告警：
		// 全新库（`applied` 为空）同样会跑 v15，但那没有任何东西被清空。
		if m.version == 15 && len(applied) > 0 {
			s.destructiveRebuildNote = "7 张表已清空（sessions/events/runs/messages/permission_memories/inputs/workspaces）；" +
				"models 保留但主键已重铸为 mdl_<ULID>（外部握着的旧模型 id 已失效）；" +
				"providers/skills/mcp_servers/automations 与 vault 原样保留（agents 表由 v19 删除）"
		}
	}
	return nil
}

// DestructiveRebuildNote 返回本进程执行过的 v15 破坏性重建的处置说明（空 = 本次没发生）。
//
// `mate doctor` 与 serve 的启动 WARN 都读它（§8.5 R1/R2）。
func (s *Store) DestructiveRebuildNote() string { return s.destructiveRebuildNote }

// backupBeforeMigration 把库文件复制成 `<db 目录>/backups/<db 文件名>.bak-<tag>-<ts>`，返回该路径。
//
// 就地改写既有数据的迁移（v12）在用户库上是**不可逆点**：一旦 v12 落地，旧二进制会因为
// `latestSchemaVersion` 前进而拒绝启动，唯一的退路就是这份文件（§回退窗口）。
// 所以它必须是"复制成功才算数"：复制失败 → **不迁移**（留在旧版本上，如实报错），
// 而不是"先迁了再说"。
//
// **落点为什么收进 `backups/` 子目录（v19）**：数据目录根下只放"当前事实"（`mate.db`、
// 配置、各域的载体目录），而迁移备份是**可丢的历史**——每前进一版就多一个文件，堆在根下
// 会让"哪个文件是活的"看起来需要判断（本机实测：根下曾累积 7 个 `mate.db.bak-v12…v17`）。
// 与 `cache/` 的思路同源：可再生的/一次性的东西不跟事实源抢位置。
//
// **老布局不搬移**：本次之前的备份仍留在库文件同级，`findMigrationBackups` 照样发现它们
// （它们是**有效的回退目标**，搬移或删除都得由用户点头）；`mate doctor` 把这类"遗留"如实
// 报出来并给出处置建议，**绝不自动删**。
//
// 内存库（path 为空）不备份：没有文件可复制，而它也不承载用户数据（测试/演示形态）。
func (s *Store) backupBeforeMigration(m migration) (string, error) {
	if s.path == "" || s.path == ":memory:" {
		return "", nil
	}
	// 空库（首次安装）没有可回退的东西：文件还没建出来、或还没有 SQLite 头。
	if fi, statErr := os.Stat(s.path); statErr != nil || fi.Size() == 0 {
		return "", nil
	}
	dir := s.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 前无法创建备份目录 %s（拒绝迁移）: %w", m.version, dir, err)
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s.bak-%s-%s",
		filepath.Base(s.path), m.backup, time.Now().UTC().Format("20060102T150405Z")))
	abs, err := filepath.Abs(dst)
	if err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 前无法解析备份路径 %s（拒绝迁移）: %w", m.version, dst, err)
	}
	// **用 SQLite 自己产快照（`VACUUM INTO`），而不是拷主库文件**（2026-10-14 实测后改）。
	//
	// 为什么拷文件是错的：`journal_mode` 是库的**持久属性**（写在文件头里），而 WAL 下已提交的
	// 事务先落 `-wal`、不立刻回写主文件——本机实测用户库头部就是 WAL（`2 2`），平台自己开的
	// 新库才是 `delete`，所以"平台不注入 pragma"并不保证打开的是回滚日志模式。此时纯拷主文件
	// 会**静默**漏掉 `-wal` 里已提交的内容（实测：主文件副本查不到那条已提交的 INSERT，带
	// `-wal` 的源库查得到）。它不报错，而文件名写着 `bak-v19-<ts>`——用户以为自己握住了
	// "迁移前那个库"，实际拿到的是更旧的一份，回退窗口在需要它的那天才发现是缺的。
	// `VACUUM INTO` 取一致性读快照并把 `-wal` 的内容并进去（同一实验里快照**含**那一行）。
	if err := s.db.Exec(`VACUUM INTO ?`, abs).Error; err != nil {
		return "", fmt.Errorf("store: 迁移 v%d 前无法产出库快照备份（拒绝迁移）: %w", m.version, err)
	}
	// `VACUUM INTO` 按 umask 建文件，权限要收口到 0600（备份与库文件同权：它装的是同一批数据）。
	if err := os.Chmod(abs, 0o600); err != nil {
		_ = os.Remove(abs)
		return "", fmt.Errorf("store: 迁移 v%d 的备份权限收口失败（拒绝迁移）: %w", m.version, err)
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
//
// v19 起**新老两个布局都列**（`backups/` 与库文件同级）：老备份仍是有效退路，把它们藏起来
// 等于让用户以为"没有回退窗口"。
func (s *Store) MigrationBackupPaths() []string { return s.findMigrationBackups("") }

// MigrationBackupPathsLegacy 只列**老布局**（库文件同级）上的迁移备份（新的在前）。
//
// 消费面是 `mate doctor` 的遗留处置提示：这些文件**不是无效的**（照旧可作回退目标），只是
// 不再按新布局落盘。所以这里只报事实与建议，不做任何自动搬移/删除——回退窗口的处置权在用户。
func (s *Store) MigrationBackupPathsLegacy() []string {
	if s.path == "" || s.path == ":memory:" {
		return nil
	}
	matches, err := filepath.Glob(s.legacyBackupGlob(""))
	if err != nil {
		return nil
	}
	return sortBackupsNewestFirst(matches)
}

// backupDirName 迁移备份子目录名（v19 起；位于库文件同级）。
const backupDirName = "backups"

// backupDir 迁移备份目录（v19 起 = `<db 目录>/backups`）。
func (s *Store) backupDir() string { return filepath.Join(filepath.Dir(s.path), backupDirName) }

// backupGlob 新布局的备份匹配式（`<db 目录>/backups/<db 文件名>.bak-<tag>-*`；tag 空 = 全版本）。
func (s *Store) backupGlob(tag string) string {
	return filepath.Join(s.backupDir(), backupGlobPattern(filepath.Base(s.path), tag))
}

// legacyBackupGlob 老布局（v19 之前）的备份匹配式：`<db 文件>.bak-<tag>-*`，与库文件同级。
func (s *Store) legacyBackupGlob(tag string) string { return backupGlobPattern(s.path, tag) }

// backupGlobPattern 拼出 `<prefix>.bak-<tag>-*` / `<prefix>.bak-v*`。
func backupGlobPattern(prefix, tag string) string {
	if tag != "" {
		return prefix + ".bak-" + tag + "-*"
	}
	return prefix + ".bak-v*"
}

// sortBackupsNewestFirst 把备份路径按**文件名**倒序（tag 与时间戳都在文件名里 ⇒ 最新在最前）。
//
// 刻意不按整串排：v19 起新老布局分处两个目录，整串排序会先按目录名分组，于是"最新"排不到
// 最前——doctor 报出的回退目标就成了老布局里那个更旧的文件（假信息，且不报错）。
func sortBackupsNewestFirst(paths []string) []string {
	out := append([]string{}, paths...)
	sort.Slice(out, func(i, j int) bool { return filepath.Base(out[i]) > filepath.Base(out[j]) })
	return out
}

// findMigrationBackups 列出库文件所在目录（含 `backups/` 子目录）里 `<db>.bak-<tag>-*` 形式的
// 备份（新的在前）。
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
	var out []string
	for _, pattern := range []string{s.backupGlob(tag), s.legacyBackupGlob(tag)} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		out = append(out, matches...)
	}
	return sortBackupsNewestFirst(out)
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
	_, maxVersion, err := s.appliedMigrationVersions(ctx)
	if err != nil {
		return 0, err
	}
	return maxVersion, nil
}
