package store

// 本文件是 **v41**（应用来源落库：`apps` 补 `source` 列）的版本步本体。
//
// 为什么需要它：「我的应用」需要区分**自己创建的应用**（`self`，含对话创建）与**从应用市场下载 /
// 装包的应用**（`market`）。来源是**持久元数据**——它记录一条定义「当初是怎么来的」，不是能从其它
// 列派生的东西（例如不能靠"本地有没有包目录"反推：包可能被手工清掉、定义仍留下），故落库而非派生。
// 计划见 `.dsh/docs/plans/2026-11-11-app-source-and-icon-plan.md` §2 B1。
//
// 取值约定（枚举二值）：
//
//	self   自建（POST /apps 新建，含对话创建）
//	market 市场下载 / 装包（InstallApp → ensurePackDefinition 新建定义）
//
// `NOT NULL DEFAULT 'self'`：存量行**全是**历史自建应用（v40 之前没有装包建定义的路径会走进这张表），
// 于是默认值就是正确的历史事实，无需一次全表 UPDATE。
//
// 语句里的表名/列名都是**字面量**（与 v15–v40 同一纪律：版本步只许写当时那一版的名字，绝不引用
// `model.TableName*` 或 Go 字段名）。
var migrationV41 = migration{
	version: 41,
	name:    "应用来源落库：apps 补 source（self/market）",
	// 备份：本步改写了既有表的定义（加列）。旧二进制对 v41 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口。与 v39/v40 同一档。
	backup: "v41",
	statements: []string{
		`ALTER TABLE apps ADD COLUMN source TEXT NOT NULL DEFAULT 'self'`,
	},
}
