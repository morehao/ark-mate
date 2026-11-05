package store

// 本文件是 **v37**（新增 `llm_calls` 表）的版本步本体。
//
// 为什么需要它：内部模型调用网关（`internal/core/llm`，设计
// `.dsh/docs/specs/2026-11-05-internal-llm-gateway-design.md` §5）的流水落库。一次调用一行、
// 可跨调用累加；`session_id` / `run_id` 可空（旁路调用无会话，这是明确设计，不是妥协）。
//
// **独立表而非扩 `runs`**：流水是独立事实（会话/旁路都可能），`runs` 是会话读模型，生命周期与
// 语义都不同——塞进去会让"删会话"牵连流水、也让"无会话的调用"无处落脚。
//
// 纯 DDL、无回填：旧库升级后本表为空（历史调用没有流水，不臆造）。
// 语句里的表名/列名都是字面量（与 v15–v36 同一纪律：版本步只许写当时那一版的名字，
// 绝不引用 `model.TableName*`）。
var migrationV37 = migration{
	version: 37,
	name:    "llm_calls（内部模型调用流水：一次调用一行，session_id/run_id 可空）",
	statements: []string{
		`CREATE TABLE llm_calls (
			id                  TEXT PRIMARY KEY,
			at                  TEXT NOT NULL,
			purpose             TEXT NOT NULL,
			session_id          TEXT NOT NULL DEFAULT '',
			run_id              TEXT NOT NULL DEFAULT '',
			provider            TEXT NOT NULL DEFAULT '',
			model               TEXT NOT NULL DEFAULT '',
			input_tokens        INTEGER NOT NULL DEFAULT 0,
			output_tokens       INTEGER NOT NULL DEFAULT 0,
			cached_read_tokens  INTEGER NOT NULL DEFAULT 0,
			cached_write_tokens INTEGER NOT NULL DEFAULT 0,
			thought_tokens      INTEGER NOT NULL DEFAULT 0,
			total_tokens        INTEGER NOT NULL DEFAULT 0,
			cost_amount         REAL NOT NULL DEFAULT 0,
			cost_currency       TEXT NOT NULL DEFAULT '',
			latency_ms          INTEGER NOT NULL DEFAULT 0,
			status              TEXT NOT NULL
		)`,
		`CREATE INDEX idx_llm_calls_at ON llm_calls(at)`,
		`CREATE INDEX idx_llm_calls_session ON llm_calls(session_id)`,
		`CREATE INDEX idx_llm_calls_purpose ON llm_calls(purpose)`,
	},
}
