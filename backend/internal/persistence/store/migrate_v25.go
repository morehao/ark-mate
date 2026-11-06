package store

// 本文件是 **v25**（容量由平台声明）的版本步本体（设计
// `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md` §5.2/§6；计划 `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md` §4.M2.T1）。
//
// 给 `models` 补两个容量列：`max_input_tokens` / `max_output_tokens`。
//
// 为什么必须补（这是路由键隔离的连带决定）：路由键加上保留前缀之后，每条路由都变成**平台手写**
// 的声明——底座内置目录（pi-ai 的 catalog）再也命中不了，于是 `contextWindow`/`maxTokens` 会落到
// dsh 的**路由级兜底**（`DEFAULT_CONTEXT_WINDOW = 262_144` / `DEFAULT_MAX_TOKENS = 32_768`，
// 源码自注 "a guess by construction"）。也就是说：隔离生效后，"容量"这件事从"恰好命中目录时
// 侥幸拿到真值"变成"平台不说就只能用一句猜测"——唯一诚实的形态是**让用户声明**。
// （注：上面那组兜底数值是 v25 当时的事实；dsh 0.2.2-rc.1 起底座兜底已变为 1_000_000/128_000。
// 本迁移只负责"让用户能声明"，不钉兜底值，故正文保留历史口径，不随底座版本改写。）
//
// 两条取值约定（**必须一起读**，否则会以为是漏了默认值）：
//
//	① **可空、无 DEFAULT**。0 在这里当不了"未声明"的哨兵：dsh 那边的 schema 是
//	   `z.number().step(1).min(1)`，写 0 会被它的校验门拒掉；而 `NOT NULL DEFAULT 0` 又逼着
//	   每一行携带一个假值。**空 = 不下发该字段**是唯一没有假值的形态。
//	② **列名不抄底座的词**。dsh 那边叫 `contextWindow`/`maxTokens`（`config.ts` 的
//	   `modelFields`）；直接拿它们当列名等于把底座词表写进内核（A9 的精神：底座私有知识不回流），
//	   而参照实现（QwenWork 的 `byok_custom_models`）用的就是"输入/输出上限"这组词。
//	   映射只写一处（adapter 的模型循环）并有单测。
//
// 两步 `ADD COLUMN` 不需要重建表（`models` 的列集合由 A1/`TestIdentifiersFollowLexicalForm`
// 按真实 schema 判，重建反而会把"当时那一版"的列集合一起改写）。
//
// 语句里的 `models` / `max_input_tokens` / `max_output_tokens` 都是**字面量**（v15–v24 同一条
// 纪律：版本步只许写当时那一版的名字，绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV25 = migration{
	version: 25,
	name:    "容量由平台声明：models 补 max_input_tokens/max_output_tokens（可空、无 DEFAULT）",
	// 备份：本步改写了既有表的定义（加列）。旧二进制对 v25 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口。与 v12/v13/v15–v24 同一档。
	backup: "v25",
	statements: []string{
		`ALTER TABLE models ADD COLUMN max_input_tokens INTEGER`,
		`ALTER TABLE models ADD COLUMN max_output_tokens INTEGER`,
	},
}
