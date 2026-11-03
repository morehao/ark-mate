package store

// 本文件是 **v33**（新增资料库目录树三表：`library_nodes` / `library_attachments` /
// `library_tables`）的版本步本体。
//
// 为什么需要它：资料库（设计 .dsh/docs/specs/2026-11-03-material-library-design.md）要给用户一棵
// **本地可寻址的目录树**——space → node，节点按 `node_kind`（folder/doc/table/link/drive/page）
// 决定载体。**字节落盘与去重直接复用已落地的附件链路**（golib `filestore` + `attachments` 表，v32）：
// 本步**不建任何 blob 表、不改造 `attachments`**，只建平台自己的「目录归属 + 节点语义」三张表。
//
// **本期范围拍板（2026-11-03）只做资料库、不做知识库**：因此**没有检索索引表**（无 embedding /
// 向量 / chunks）。「找」只做结构浏览 + 关键词子串匹配，直接查 `library_nodes.title` + `body` 的
// LIKE，不需要独立索引表。详见设计 §0 眉批、§4。
//
// 纯 DDL、无数据回填：旧库升级后三张表为空表，面板显示「还没有资料」。
// 语句里的表名 / 列名都是**字面量**（与 v15–v32 同一条纪律：版本步只许写当时那一版的名字，
// 绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV33 = migration{
	version: 33,
	name:    "新增资料库目录树三表（library_nodes / library_attachments / library_tables）",
	statements: []string{
		`CREATE TABLE library_nodes (
			id              TEXT PRIMARY KEY,
			space_id        TEXT NOT NULL,
			parent_id       TEXT NOT NULL DEFAULT '',
			node_kind       TEXT NOT NULL,
			title           TEXT NOT NULL,
			body            TEXT NOT NULL DEFAULT '',
			table_id        TEXT NOT NULL DEFAULT '',
			drive_upload_id TEXT NOT NULL DEFAULT '',
			source_url      TEXT NOT NULL DEFAULT '',
			size_bytes      INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		)`,
		// idx_library_nodes_space_parent：列目录（按 space + 父节点取一层子节点）。
		`CREATE INDEX idx_library_nodes_space_parent ON library_nodes(space_id, parent_id)`,
		// idx_library_nodes_kind：按种类筛（前端按类型分栏 / 守卫对账）。
		`CREATE INDEX idx_library_nodes_kind ON library_nodes(node_kind)`,
		// idx_library_nodes_title：关键词检索的前缀辅助（LIKE 走前缀时可用索引）。
		`CREATE INDEX idx_library_nodes_title ON library_nodes(title)`,
		// idx_library_nodes_drive_upload：drive 节点反查其附件/字节。
		`CREATE INDEX idx_library_nodes_drive_upload ON library_nodes(drive_upload_id)`,

		// library_attachments：节点 ↔ 附件关联（不改造 attachments 表）。
		// 一节点可挂多附件；role=primary 是 drive 节点的内容本体，attachment 是附属。
		`CREATE TABLE library_attachments (
			node_id       TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			role          TEXT NOT NULL,
			created_at    TEXT NOT NULL,
			PRIMARY KEY (node_id, attachment_id)
		)`,
		// idx_library_attachments_attachment：按附件反查挂到了哪些节点。
		`CREATE INDEX idx_library_attachments_attachment ON library_attachments(attachment_id)`,

		// library_tables：table 节点的 schema + 行（首版单列 JSON，结构化 CRUD 在用例层做）。
		`CREATE TABLE library_tables (
			node_id     TEXT PRIMARY KEY,
			schema_json TEXT NOT NULL,
			rows_json   TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`,
	},
}
