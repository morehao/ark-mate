package store

// 本文件是 **v31**（MCP 网关调用审计表）的版本步本体（T2 第一刀 / O3-b）。
//
// 为什么要有它：网关今天只在 stderr 留一行 `(server, tool, outcome, latency_ms)`——那行日志
// 回答"刚才发生了什么"，回答不了"**这个会话**到底调了什么"。日志按日期轮转、进程一退就散，
// 无法按会话聚合复盘；而"某轮里模型调了哪台 server 的哪个工具、失败了几次、慢在哪"是排查
// 工具面问题的第一手材料（设计 §10.6-⑤ 的 E4/E6 也从这条线取证）。
//
// 三件刻意的事：
//
//	① **纯建表**：不动任何既有行，所以不需要 `backup`（回退靠"换回旧二进制 + 恢复升级前那份
//	   库备份"，与其余纯 DDL 步同一档；旧二进制读到更高 schema 版本会拒绝启动，这是既有约定）；
//	② **不记工具入参正文**：入参里常带路径与令牌，落库等于把库变成第二份凭据面（T2 风险段）。
//	   要摘要就另开一列并写清算法，不要在 `tool`/`error_kind` 里塞 JSON；
//	③ **两张索引**：`(session_id, id)` 服务"按会话倒序取最近若干行"的复盘读面，`created_at`
//	   服务保留期裁剪。`id` 进 `session_id` 索引的第二位是**排序方向**的载体——审计表只按
//	   写入序倒序读，不按时间戳排序（时间列只做范围裁剪）。
var migrationV31 = migration{
	version: 31,
	name:    "MCP 网关调用审计表（mcp_call_audits）",
	statements: []string{
		`CREATE TABLE mcp_call_audits (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL DEFAULT '',
			run_id     TEXT NOT NULL DEFAULT '',
			server_id  TEXT NOT NULL DEFAULT '',
			tool       TEXT NOT NULL,
			outcome    TEXT NOT NULL,
			error_kind TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_mcp_call_audits_session ON mcp_call_audits(session_id, id)`,
		`CREATE INDEX idx_mcp_call_audits_created ON mcp_call_audits(created_at)`,
	},
}
