package store

// 本文件是 **v38**（给 `inputs` 加 `attachments` 列）的版本步本体。
//
// 为什么需要它：`inputs` 表（§6 忙时队列）此前只存 `text` / `delivery`——忙时带附件排队会在
// 入队时把附件丢掉（附件 id 无处可存，投递时也无从还原），而"我排了队、附件却没了"是静默的数据
// 丢失。v4 把队列差距补齐：入队时把本轮附件 id 列表（JSON 数组）随队项落库，投递时原样带出。
//
// **存 id 列表而非字节**：附件字节在附件链路（filestore）里，队项只引用 `att_…`；投递时
// 由既有附件解析路径读回。故本列是 `[]string` 的 JSON 序列化，空串 = 无附件（历史行零迁移语义）。
//
// 纯 DDL、无回填：旧库升级后既有 inputs 行的 `attachments` 取默认 ''（无附件）。
// 表名/列名都是字面量（与 v15–v37 同一纪律：版本步只写当时那一版的名字，不引用 `model.TableName*`）。
var migrationV38 = migration{
	version: 38,
	name:    "inputs 加 attachments 列（忙时排队携带附件 id，v4 队列差距补齐）",
	statements: []string{
		// attachments：本轮附件 id 列表（JSON 数组，如 ["att_a","att_b"]）。空串 = 无附件。
		`ALTER TABLE inputs ADD COLUMN attachments TEXT NOT NULL DEFAULT ''`,
	},
}
