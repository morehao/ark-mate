package store

// 本文件是 **v36**（给 `runs` 加 `token_usage_json` 列）的版本步本体。
//
// 为什么需要它：此前 run 只落 `usage_json`（上下文窗口**快照**：used/size/cost，同会话后到覆盖
// 先到、不可相加）。ACP 的 `PromptResponse.usage` 其实带**本轮完整 token 明细**（input/output/
// cached/thought/total），但客户端长期丢弃。设计 `.dsh/docs/specs/2026-11-05-internal-llm-gateway-design.md`
// §0.2/§2 起把它接住，需要一个与快照**分开**的列来存流水语义（每轮一条、可累加）。
//
// **刻意新增独立列而非折进 `usage_json`**：快照与流水语义相反（前者后到覆盖先到、后者逐轮累加），
// 混一列会让"相加"变成静默错误。两者的边界见 `objusage` 包注释。
//
// 纯 DDL、无回填：旧库升级后既有 runs 行的 `token_usage_json` 取默认 `”`（表示「未上报」）。
// 语句里的表名/列名都是字面量（与 v15–v35 同一纪律：版本步只许写当时那一版的名字，
// 绝不引用 `model.TableName*`）。
var migrationV36 = migration{
	version: 36,
	name:    "runs 加 token_usage_json 列（逐轮 token 明细流水，与 usage_json 上下文快照分开）",
	statements: []string{
		// token_usage_json：本轮 token 明细（JSON，形如
		// {"input_tokens":..,"output_tokens":..,"cached_read_tokens":..,...,"total_tokens":..}）。
		// 空串 = 底座未上报（不臆造零消耗）。
		`ALTER TABLE runs ADD COLUMN token_usage_json TEXT NOT NULL DEFAULT ''`,
	},
}
