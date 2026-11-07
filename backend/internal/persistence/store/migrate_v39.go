package store

// 本文件是 **v39**（模型思考能力由平台声明）的版本步本体（设计
// `.dsh/docs/specs/2026-11-07-workbuddy-settings-reverse-design.md` §6；计划
// `.dsh/docs/plans/2026-11-07-workbuddy-settings-landing-plan.md` §1）。
//
// 给 `models` 补三列思考能力声明：
//
//	supports_reasoning  是否声明为思考模型（0/1）
//	reasoning_efforts   声明支持的思考强度档位（逗号分隔规范序，如 `low,medium,high`；空=未声明档）
//	default_effort      默认思考强度（空=自动，由请求层默认）
//
// 为什么必须补：底座 dsh 的模型 schema **原生支持** reasoning（`reasoningEfforts:{档位→wire值}`
// ＋ profile 级 `reasoning:<默认档>`，源 `@deepseek-ai/dsh-llm-pi-ai`）。不声明这三列，用户就没法
// 把"这是思考模型、支持哪些强度、默认哪一档"如实下发——只能靠底座默认。
//
// 诚实口径（reverse-design §6）：仅「思考」能如实落到 dsh；「工具调用 / 自定义协议」底座无对应
// 模型字段，本迁移**不引入**那两列（宁缺毋假）。
//
// 取值约定：
//
//	① `supports_reasoning` **NOT NULL DEFAULT 0**：三态没有意义，"没勾"就是"不思考"，bool 明确。
//	② `reasoning_efforts` / `default_effort` **NOT NULL DEFAULT ''**：空串 = 未声明。思考强度不是
//	   可空指针业务（空串已是明确的"未声明"），故用非空文本给存量行一个确定默认。
//
// 三条 `ADD COLUMN` 追加在物理列末尾（`TestCapabilityDefinitionColumnsFrozen` 比物理列序，白名单
// 也按末尾追加，见 capability_test.go）。语句里的表名列名都是**字面量**（v15–v24 同一条纪律：
// 版本步只许写当时那一版的名字，绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV39 = migration{
	version: 39,
	name:    "思考能力由平台声明：models 补 supports_reasoning/reasoning_efforts/default_effort",
	// 备份：本步改写了既有表的定义（加列）。旧二进制对 v39 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口。与 v12/v13/v15–v25 同一档。
	backup: "v39",
	statements: []string{
		`ALTER TABLE models ADD COLUMN supports_reasoning INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE models ADD COLUMN reasoning_efforts TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE models ADD COLUMN default_effort TEXT NOT NULL DEFAULT ''`,
	},
}
