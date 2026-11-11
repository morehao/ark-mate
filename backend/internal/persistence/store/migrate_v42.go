package store

// 本文件是 **v42**（应用使用说明落库：`apps` 补 `tagline` / `examples_json` / `usage_guide` 三列）
// 的版本步本体。
//
// 为什么需要它：应用是「组合 + 扩展点」的技术对象，过去读面只展示它的**接线**（工具域 / slot /
// 成员 / route）——用户看到应用不知道该怎样通过自然语言使用它。这三列承载「怎么开口」的产品面：
//
//	tagline       一句话「对它说什么」的能力句（≤40 字），列表/详情主文案
//	examples_json 可直接发送的示例话术数组（≤6 条、每条 ≤100 字）
//	usage_guide   「怎么用」正文（≤600 字）：适用场景 / 需要提供什么 / 产出 / 边界
//
// 三列都带 `NOT NULL DEFAULT`（''/'[]'/''）：存量行没有使用说明，默认空即正确历史事实，无需全表 UPDATE；
// 前端对空值做「从 description / 成员派生兜底」，不空转。
//
// 语句里的表名/列名都是**字面量**（与 v15–v41 同一纪律：版本步只许写当时那一版的名字，绝不引用
// `model.TableName*` 或 Go 字段名）。
var migrationV42 = migration{
	version: 42,
	name:    "应用使用说明落库：apps 补 tagline/examples_json/usage_guide",
	// 备份：本步改写了既有表的定义（加列）。旧二进制对 v42 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口。与 v39/v40/v41 同一档。
	backup: "v42",
	statements: []string{
		`ALTER TABLE apps ADD COLUMN tagline TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE apps ADD COLUMN examples_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE apps ADD COLUMN usage_guide TEXT NOT NULL DEFAULT ''`,
	},
}
