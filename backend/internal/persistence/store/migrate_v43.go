package store

// 本文件是 **v43**（会话最近活跃时间 `sessions.last_active_at` 落库）的版本步本体（SaaS 池化 M3-3/M3-1）。
//
// 为什么需要它：池化的「热/温/冷分层」（M3-3 的 TieringPolicy）与「驻留 LRU 调度」（M3-1 的
// ResidencyPolicy）都以「最近活跃时间」为判决输入；而现有 `updated_at` 是**行 mtime**——改标题/置顶/
// 归档都会刷新它，不代表「会话被真正使用」。用一个专用列把「最近一次活跃（一轮 prompt / resume
// 使用）」与「元数据变更」分开，调度才有干净的信号。
//
// 语义与口径：
//   - Unix **毫秒**（与本仓瞬时口径 objtime.Milli 一致，与 created_at/updated_at 的 RFC3339 TEXT 不同——
//     那两列是落库通用时间，本列是调度信号，量纲跟随 wire 约定）；
//   - `DEFAULT 0` = 从未记录（存量行 / 新建未活跃）——TieringPolicy/Residency 对零值按「无活跃信号」
//     保守处理（分层判热、LRU 视作最久远），故无需为存量行回填。
//
// 纯增列（有 DEFAULT），不需要备份。语句里的表名/列名是**字面量**（与 v15–v42 同一纪律）。
var migrationV43 = migration{
	version: 43,
	name:    "会话最近活跃时间落库（last_active_at，Unix 毫秒；0 = 未记录）",
	statements: []string{
		`ALTER TABLE sessions ADD COLUMN last_active_at INTEGER NOT NULL DEFAULT 0`,
	},
}
