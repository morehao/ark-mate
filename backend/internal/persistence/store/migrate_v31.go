package store

// 本文件是 **v31**（新增 `experts` 专家定义表）的版本步本体。
//
// 为什么需要它：会话发起时的「专家」是一条命名的会话级能力预设（推荐哪些技能/MCP/模型，
// P1 起可带角色设定）。它沿用 skill/mcp_servers 的**定义载体**形态：`body` 存原始 JSON
// （未知键原样保留）、`revision` 是写面 CAS 判据、`updated_at` 是列表排序依据。
//
// 纯 DDL、无数据回填：旧库升级后 `experts` 为空表，面板显示「还没有专家」。
// `sessions.capability_json` / `workspaces.default_capability_json` **不加列**：专家 id 写在那两个
// 既有 JSON 列里，旧二进制解码时忽略未知键；而带专家的会话在创建时已把技能/MCP**物化**成具体
// id 列表，所以旧二进制的有效能力不变（设计 §迁移与兼容）。
//
// 语句里的 `experts` / 列名都是**字面量**（与 v15–v30 同一条纪律：版本步只许写当时那一版的名字，
// 绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV31 = migration{
	version: 31,
	name:    "新增 experts 专家定义表（body + revision + updated_at）",
	statements: []string{
		`CREATE TABLE experts (
			id         TEXT PRIMARY KEY,
			body       TEXT NOT NULL,
			revision   INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	},
}
