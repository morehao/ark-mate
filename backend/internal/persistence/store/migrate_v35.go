package store

// 本文件是 **v35**（给 `artifacts` 加 `path` 列）的版本步本体。
//
// 为什么需要它：产物自动抽取（P2，.dsh/docs/plans/2026-11-04-artifact-auto-extraction-plan.md §5）
// 的 dirty 惰性判定要「比对工作区当前文件与生成时快照」，需要知道**原文件在工作区里的相对路径**。
// v34 的 `artifacts` 只存 `title`（basename），子目录文件定位不到——故 v35 补 `path`（相对会话
// cwd 的工作区路径；显式上传/内联产物为空）。
//
// 纯 DDL、无回填：旧库升级后既有 `artifacts` 行的 `path` 取默认值 `”`（表示「无工作区原文件」，
// dirty 判定对这类行跳过）。语句里的表名/列名都是字面量（与 v15–v34 同一纪律：版本步只许写
// 当时那一版的名字，绝不引用 `model.TableName*`）。
var migrationV35 = migration{
	version: 35,
	name:    "artifacts 加 path 列（工作区相对路径，供 dirty 惰性判定定位原文件）",
	statements: []string{
		// path：工作区相对路径（相对会话 cwd）。仅「工具写工作区文件」得来的 file 类产物有值；
		// 显式上传/内联产物为空串（无工作区原文件）。
		`ALTER TABLE artifacts ADD COLUMN path TEXT NOT NULL DEFAULT ''`,
	},
}
