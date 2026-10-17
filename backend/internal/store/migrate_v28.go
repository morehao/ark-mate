package store

// 本文件是 **v28**（权限记忆带上**授权形态**）的版本步本体（P2-a；`docs/unimplemented.md` O16、
// 路线 L3-2「权限的『总是允许』绑到当初那次具体授权，而不是一个布尔标志」）。
//
// 两列，都是 `ADD COLUMN`（**不重建表**）：
//
//	scope             授权形态。今天只有一个可写值 `session`（= 会话内始终）。
//	                  **刻意不把"一次"做成一个值**：`once` 决策本来就不落库（见 `Remember` 的注释），
//	                  给它留一个永远不会被写入的枚举值，只会让下一个读它的人以为平台支持那种记忆。
//	grant_input_digest 当初那次授权**那条工具入参**的摘要（审计用，**不进唯一键**）。
//
// **唯一键刻意不变**（仍是 `(session_id,title,kind)`）：把入参并进键会让"这类工具始终允许"
// 退化成"这一个参数组合始终允许"——那是把一条便利功能拆掉，而不是修一个缺陷。
// 摘要的作用是**可复核**：出事时能回答"当初那句话，是在哪次具体调用上说的"。
var migrationV28 = migration{
	version: 28,
	name:    "权限记忆带上授权形态（scope + grant_input_digest；唯一键不变）",
	// 纯增列（两列都有 DEFAULT），不需要备份。
	statements: []string{
		`ALTER TABLE permission_memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'session'`,
		`ALTER TABLE permission_memories ADD COLUMN grant_input_digest TEXT NOT NULL DEFAULT ''`,
	},
}
