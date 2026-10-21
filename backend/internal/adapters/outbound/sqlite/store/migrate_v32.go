package store

// 本文件是 **v32**（定义变更审计的哈希链）的版本步本体（T4 / L3-3）。
//
// 为什么需要它：`definition_revisions` 是"谁在什么时候把哪一版定义改成了什么"的唯一记账，而
// 到今天为止**每行都是独立的**——有人在库里改一行的 `digest` 或 `actor`，看不出任何异常：
// 自增 `seq` 照样连续、行数照样对。哈希链把"改动"从**不可见**变成**可定位**（算法与归一化规则
// 写在 `definition_revision_chain.go`，那里也是校验函数的所在地）。
//
// 两件刻意的事：
//
//	① **纯增列**：两列都带 `DEFAULT ''`，既有行一个字节都不动，故不需要 `backup`（回退仍是
//	   "换回旧二进制 + 恢复升级前那份库备份"，与其余纯 DDL 步同一档）；
//	② **不给历史行补算哈希**：v32 之前的行 `hash` 留空，构成链的**前缀**——不参与校验，也不
//	   假装当时被校验过。补算等于把算法固化进一条历史迁移，而且"这些行到底有没有被改过"这个
//	   事实从此永远无法回答（补算后它们看起来都"对"）。代价如实写下来：**v32 之前的改动查不出来**。
//
// DefinitionRevisionChainSchemaVersion 哈希链落地的那一步（`mate doctor` 用它判"这个库有没有链"，
// 不写字面量——版本号只有一个事实源）。它与 `migrationV32.version` 的一致性由
// `definition_revision_chain_test.go` 的一条断言钉住。
const DefinitionRevisionChainSchemaVersion = 32

var migrationV32 = migration{
	version: 32,
	name:    "definition_revisions 哈希链（prev_hash / hash；历史行为空前缀）",
	statements: []string{
		`ALTER TABLE definition_revisions ADD COLUMN prev_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE definition_revisions ADD COLUMN hash TEXT NOT NULL DEFAULT ''`,
	},
}
