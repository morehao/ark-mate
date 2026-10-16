package store

// 本文件是 **v24**（命名收口）的版本步本体（设计
// `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md` §5.3/§6；计划 `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md` §4.M1.T1）。
//
// 只有一件事：`models.model_id` → `models.model`。
//
// 为什么这一列必须改名（而不是留着不动）：`model_id` 在同一个库里是**一词三义**——
//
//	① `models.model_id`  = 上游真实模型名（外部世界的名字）；
//	② `models.id`        = 平台铸造的行身份（`mdl_<ULID>`）；
//	③ `sessions.model_id` = 会话的模型引用。
//
// ①②并存却无消歧词，就是用户 2026-10-15 那句"`models` 中 `id` 和 `model_id`
// 两个字段也很有歧义"。改名之后 `_id` 一律指"指向某表 `id`"（`provider_id` → `providers.id`、
// `sessions.model_id` → `models.id`），而 `model` 是"发给上游的那个名字"——这也是生态标准词
// （OpenAI 请求体 `{"model": …}`、Continue `models[].model`、QwenWork `byok_custom_models.model`，
// 自注"上游真实模型名"）。
//
// **本步是纯改名，零行为变化**：值逐字不动、行数不动、索引名不动（`RENAME COLUMN` 会改写引用
// 该列的索引定义，v16/v17 已有先例，见 `migrate.go` 的 v16 步）。计划 E2 把"改名"与"改行为"
// 分开：验收判据就是 `git diff testdata/` 为空——凡发现行为漂移，问题一定在本步。
//
// 语句里的 `models` / `model_id` / `model` 都是**字面量**（v15–v23 同一条纪律：版本步只许写
// **当时那一版**的名字，绝不引用 `model.TableName*` 或 Go 字段名——引用会让实体改名把这条历史
// 步骤一起改掉，判据 `TestMigrationStepsUseHistoricalLiterals`）。
var migrationV24 = migration{
	version: 24,
	name:    "命名收口：models.model_id 改名 model（上游真实模型名那一列）",
	// 备份：本步改写了既有表的定义（列名是 schema 的一部分）。旧二进制对 v24 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口，`mate doctor` 会打印它。
	// 与 v12/v13/v15/v16/v17/v19/v20/v21/v22/v23 同一档。
	backup: "v24",
	statements: []string{
		// 唯一一步 DDL。`ALTER TABLE … RENAME COLUMN` 是 SQLite 3.25+ 的能力，且**不需要重建表**
		// ——重建会把"当时那一版"的列集合一起改写掉，而 A1/`TestIdentifiersFollowLexicalForm`
		// 判据读的就是真实 schema。
		`ALTER TABLE models RENAME COLUMN model_id TO model`,
	},
}
