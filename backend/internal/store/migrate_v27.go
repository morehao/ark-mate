package store

// 本文件是 **v27**（工作空间默认值 + 权限记忆带作用域）的版本步本体（设计 ADR-7 / ADR-16 ①，
// 计划 `.dsh/docs/specs/2026-10-16-workbuddy-alignment-execution-plan.md` 的 P1-f）。
//
// 两件事：
//
//	① `workspaces` 补两列**默认值**：`default_capability_json` / `default_permission_mode`。
//	   以前"建会话不写 capabilities / 权限模式"等于**用平台缺省**，用户每开一个会话都得重挑一遍。
//	   两列都是 `NOT NULL DEFAULT ''`，`''` = **这一档没有工作空间默认值**（回落到平台缺省）。
//	   这与"空串是合法取值"不冲突：能力集空串不是一种能力集，权限模式空串也不是一种模式。
//
// **与计划的一处偏离（如实写）**：计划把 `permission_memories` 的 `scope` 重建也塞进 v27，
// 理由是"两件事都是本批要动的 schema，一次备份省一个窗口"。实现时把它挪到了 **v28（P2-a）**：
// 加一个**没有任何消费者**的列、同时 DAO 的唯一键必须跟着变（`Upsert` 的 `ON CONFLICT` 现在指着
// 三元组），两件事分在两批就必然出现"这一批绿、下一批红"的中间态。而"一列 + 用它的那个写面"
// 属于同一件事，应该在同一批里落地。
//
// **本步一个字都不改 `sessions`**（这是本批最要紧的一条纪律）：工作空间默认值只影响**以后**建的
// 会话。既有会话的能力集与权限模式是**快照**（`sessions.capability_json` /
// `permission_policy.native_mode`），改了工作空间默认值就把它们悄悄改掉，等于"我调了个默认值，
// 昨天那个会话的行为变了"——那是本仓最忌的那类静默变更。判定这条的守卫是
// `TestWorkspaceDefaultsDoNotRewriteSessions`（它同时禁止 `SetWorkspaceDefaults` 里出现 UPDATE sessions）。
//
// 语句里的 `workspaces` / `permission_memories` / 列名都是**字面量**（v15–v26 同一条纪律：版本步
// 只许写当时那一版的名字，绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV27 = migration{
	version: 27,
	name:    "工作空间补两个默认值列（default_capability_json / default_permission_mode）",
	// 备份：本步**重建一张表**（`permission_memories`）——`backups/<db>.bak-v27-<ts>` 是唯一的
	// 回退窗口（旧二进制对 v27 库拒绝启动）。
	backup: "v27",
	statements: []string{
		// 1. 工作空间的两个默认值列。
		//
		//    `NOT NULL DEFAULT ''` 而不是可空：两列都用**空串**表达"没有默认值"，
		//    于是读面不需要区分 NULL 与空串两种"没有"（那是同一件事的两种写法，
		//    而两种写法迟早会在某个 `WHERE ... IS NOT NULL` 里分叉）。
		`ALTER TABLE workspaces ADD COLUMN default_capability_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workspaces ADD COLUMN default_permission_mode TEXT NOT NULL DEFAULT ''`,

		// 2. （**permission_memories 的 `scope` 重建不在本步**，见文件头「与计划的一处偏离」）
	},
}
