package store

// 本文件是 **v33**（自动化触发的事务性 outbox）的版本步本体（T5 / L3-4）。
//
// 为什么需要它：automation 的"跑完"与"结果送达"是同一条路——`host.CreateSession` 一失败，
// 那一轮就只剩一句 reason 留在 `recent_runs` 里，**没有可重试的落点**。底座一时不可用（重启、
// 端口占用、恢复中）是常态而非例外，而这条路的后果是"今晚的巡检永久丢掉一次"。
//
// 本表把"投递"从执行路径上摘出来：执行侧只写一行 pending，worker 按退避重试，投成一次即
// `sent`。三条设计约束写在这里，改表前先读：
//
//	① **幂等键库级唯一**（`idempotency_key = automation_id|occurrence|trigger`）：同一个
//	   occurrence 重复入队只会有一行——"补投且只投一次"的第一道闸（第二道是投递前的状态 CAS）。
//	② **不存运行结果**：只存投递入参（prompt/cwd/model/origin）。结果在会话里（ADR-4 不变）。
//	③ **状态只有三个**（pending / sent / failed）：attempts 用尽才进 failed，且那一步要 ERROR
//	   留痕——"默默放弃"和"还在重试"在面板上看起来一样，是最难查的一类。
var migrationV33 = migration{
	version: 33,
	name:    "automation 投递队列（automation_outbox：幂等键 + 退避重试）",
	// 纯建表，不改既有行，故不需要备份。
	statements: []string{
		`CREATE TABLE automation_outbox (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			automation_id   TEXT NOT NULL,
			occurrence      TEXT NOT NULL DEFAULT '',
			trigger         TEXT NOT NULL,
			payload         TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			status          TEXT NOT NULL,
			attempts        INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT NOT NULL,
			last_error      TEXT NOT NULL DEFAULT '',
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_automation_outbox_idem ON automation_outbox(idempotency_key)`,
		`CREATE INDEX idx_automation_outbox_due ON automation_outbox(status, next_attempt_at)`,
	},
}
