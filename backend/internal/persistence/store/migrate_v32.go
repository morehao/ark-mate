package store

// 本文件是 **v32**（新增 `attachments` 附件业务索引表）的版本步本体。
//
// 为什么需要它：附件链路（设计 .dsh/docs/specs/2026-11-02-attachment-pipeline-design.md §3）
// 把「字节落盘 / 内容寻址去重 / core_file·core_file_upload 两表」全部复用 golib `filestore`；
// 但 filestore 不知道「归属哪个会话」「投递形态（image/document）」「pending/linked/orphan 生命周期」
// ——这些是 agent 平台的业务语义，必须平台自己存一张表。
//
// **`core_file` / `core_file_upload` 不在这里建**：它们的 DDL 事实源在 golib 组件，
// 由启动期 `filestore.Migrate(db)`（AutoMigrate，幂等）建。这是本仓「不用 gorm AutoMigrate」
// 那条纪律的一个**显式例外**，且只对 core_file* 两张表——已登记进 docs/agents/build-and-runtime.md。
// `attachments`（平台自己的表）仍走自管迁移，就是本步。
//
// 纯 DDL、无数据回填：旧库升级后 `attachments` 为空表，面板/接口显示「还没有附件」。
// 语句里的 `attachments` / 列名都是**字面量**（与 v15–v31 同一条纪律：版本步只许写当时那一版的名字，
// 绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV32 = migration{
	version: 32,
	name:    "新增 attachments 附件业务索引表（会话归属 + 投递形态 + 生命周期）",
	statements: []string{
		`CREATE TABLE attachments (
			id         TEXT PRIMARY KEY,
			upload_id  TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			name       TEXT NOT NULL,
			mime_type  TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			sha256     TEXT NOT NULL,
			kind       TEXT NOT NULL,
			state      TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		// idx_attachments_session：会话详情读附件用。
		`CREATE INDEX idx_attachments_session ON attachments(session_id)`,
		// idx_attachments_upload：反查 filestore 记录用（附件行 ↔ core_file_upload 的对账）。
		`CREATE INDEX idx_attachments_upload ON attachments(upload_id)`,
		// idx_attachments_sha256：按内容查附件（去重/审计）。
		`CREATE INDEX idx_attachments_sha256 ON attachments(sha256)`,
		// idx_attachments_state_created：孤儿清扫扫描（state=pending AND created_at < now-TTL）用。
		`CREATE INDEX idx_attachments_state_created ON attachments(state, created_at)`,
	},
}
