package store

// 本文件是 **v34**（新增产物表 `artifacts` + 给 `library_nodes` 加 `category`/`provenance_id` 两列）
// 的版本步本体。
//
// 为什么需要它：产物溯源设计（.dsh/docs/specs/2026-11-03-artifact-provenance-design.md §4）要给
// 「会话/工具产出的东西」一张**溯源登记册** `artifacts`——它不存字节（字节复用 golib filestore 的
// core_file/core_file_upload），只记「哪次会话/哪一轮/哪个工具调用产出的、生成时内容哈希（快照）、
// 后来是否被改动（dirty）」。资料库 `library_nodes` 通过 `provenance_id` **引用**产物（非复制），
// `category` 区分「用户自建资料(material)」与「会话产物(artifact)」。
//
// 纯 DDL、无数据回填：旧库升级后 `artifacts` 为空表，`library_nodes` 两列取默认值
// （category='material'、provenance_id=”）。语句里的表名/列名都是字面量（与 v15–v33 同一纪律：
// 版本步只许写当时那一版的名字，绝不引用 `model.TableName*`）。
var migrationV34 = migration{
	version: 34,
	name:    "新增产物表 artifacts + library_nodes 加 category/provenance_id 两列",
	statements: []string{
		// artifacts：产物登记册（溯源事实源；不存字节）。
		`CREATE TABLE artifacts (
			id             TEXT PRIMARY KEY,
			session_id     TEXT NOT NULL,
			run_id         TEXT NOT NULL DEFAULT '',
			tool_call_id   TEXT NOT NULL DEFAULT '',
			origin         TEXT NOT NULL,
			kind           TEXT NOT NULL,
			title          TEXT NOT NULL,
			mime_type      TEXT NOT NULL DEFAULT '',
			size_bytes     INTEGER NOT NULL DEFAULT 0,
			upload_id      TEXT NOT NULL DEFAULT '',
			content_text   TEXT NOT NULL DEFAULT '',
			content_sha256 TEXT NOT NULL DEFAULT '',
			dirty          INTEGER NOT NULL DEFAULT 0,
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL
		)`,
		// idx_artifacts_session：按会话列产物（GET /sessions/:id/artifacts）。
		`CREATE INDEX idx_artifacts_session ON artifacts(session_id)`,
		// idx_artifacts_run：按轮次反查产物（哪一轮产出了什么）。
		`CREATE INDEX idx_artifacts_run ON artifacts(run_id)`,
		// idx_artifacts_upload：按 file_id 反查产物（file 类产物与 core_file_upload 对账）。
		`CREATE INDEX idx_artifacts_upload ON artifacts(upload_id)`,

		// library_nodes 加两列：分类维度 + 引用产物主键（可空串）。
		`ALTER TABLE library_nodes ADD COLUMN category TEXT NOT NULL DEFAULT 'material'`,
		`ALTER TABLE library_nodes ADD COLUMN provenance_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX idx_library_nodes_category ON library_nodes(category)`,
		`CREATE INDEX idx_library_nodes_provenance ON library_nodes(provenance_id)`,
	},
}
