package store

// 本文件是 **v21 的数据迁移**（设计 `.dsh/docs/specs/2026-10-14-data-model-v21-design.md` 的 D1／§7）。
//
// 它把 `events.seq` 从「全局位次」改成「**会话内**位次」（0 起、稠密），并把号序计数器独立成
// `event_sequence` 表（opencode 的 `event_sequence` 形状）。全部步骤在同一个事务里
// （失败整体回滚，不存在"迁移了一半"）。
//
// **顺序是语义必需，不是排版**（本文件最容易写错的一处）：
//
//	① 建 `event_sequence` 计数器表；
//	② **先在旧位次上重编号读模型窗口列**（`runs.first_seq/last_seq`、`messages.seq_lo/seq_hi`）；
//	③ 再重建 `events`（`ROW_NUMBER()` 分会话重编号，`seq` 从 0 起）；
//	④ 回填计数器；⑤ 对齐 `sessions.last_seq`；⑥ 清全局计数键、写读模型重建标记。
//
// 为什么 ② 必须在 ③ **之前**：这一步做的是**映射**（旧全局 seq → 新会话内位次），而 ③ 一旦执行，
// 旧位次就整个从库里消失了——映射失去参照。反过来写（在重建之后用
// `COUNT(*) … e.seq < runs.first_seq`）会把"新位次"与"旧位次"两套数放在一起比较，产物是
// **静默错位**的窗口列：不报错、不违反任何约束，只是每条 run 的窗口都指向别的轮次。
// 计划 §4.M1.T1 的 SQL 片段恰好把它排在重建之后，这里按语义修正（映射只对
// `COUNT(*) WHERE e.seq < <旧位次>` 这一种写法成立——第一个事件恰好有 0 条事件在它之前，故新位次 = 严格小于它的条数）。
//
// 为什么 `runs.id` 本步不动：它此刻仍是那个过渡态的不透明串 `r-<session>-<旧位次>`。
// M2 会统一换成 `run_<ULID>` 并把"每会话每起点一条"变成 DB 不变式；本步只负责让窗口列
// 落在**会话内位次**区间里（判据：`migrate_v21_test.go`）。
//
// 语句全是**字面量**（与 v15–v17/v19/v20 同一条纪律）：这里出现的 `events_last_seq` /
// `idx_events_seq` / `events_v21` 都是**被改掉的那一版**的名字，绝不引用 `model.TableName*`
// 常量——引用会让实体改名把这条历史步骤一起改掉（守卫⑤ `TestMigrationStepsUseHistoricalLiterals`）。
//
// 为什么本步有自己的文件（而不是像 v19/v20 那样把 statements 内联进 `migrate.go`）：它的
// statements **自带一段必须与人讲清楚的顺序理由**（上面那段），而 v12/v13/v14/v15/v16/v17 的
// 版本步文件正是同一形状（判据 `TestDestructiveRebuildIsOneShot` 扫的也是 `migrate_v*.go`）。
var migrationV21 = migration{
	version: 21,
	name:    "位次回归每会话：events.seq 分会话重编号（0 起）+ event_sequence 计数器 + 删全局计数键",
	// 备份：本步**就地重建 `events`**（把既有行的 seq 整列换义）并改写 runs/messages 的窗口列
	// ——它是"就地改写既有数据"，与 v12/v13/v15/v16/v17/v19/v20 同一档。旧二进制对 v21 库是
	// **拒绝启动**（`latestSchemaVersion` 前进），所以备份是唯一的回退窗口。
	backup: "v21",
	statements: []string{
		// ——— ① 每会话号序计数器（照 opencode 的 `event_sequence(aggregate_id PK, seq)`）———
		//
		// `seq` 的语义是"该会话**已分配到的**位次"（刚建会话还没有行 → 第一次取号插入 0，
		// 于是会话的第一条事件 seq = 0，与 `session.created` 是第 0 条事件这件事一致）。
		//
		// 主键是 `session_id` 而不是 `id`：本表没有自己的身份，它就是那个会话的计数器
		// （与 `schema_migrations.version` 同形，gormdao 允许主键列不叫 id）。
		`CREATE TABLE event_sequence (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id),
			seq        INTEGER NOT NULL DEFAULT 0
		)`,

		// ——— ② 读模型窗口列就地重编号（**必须在 ③ 之前**：用的是旧全局位次）———
		//
		// 语义等价于窗口函数，只是 `UPDATE` 里写不了 `ROW_NUMBER`：全局 seq 全局单调 ⇒
		// 会话内单调，所以"新位次 = 该事件之前（不含自己）的同会话事件条数"是**确定**的映射，
		// 不是启发式。
		//
		// 刻意**不加** `WHERE first_seq > 0`：没有窗口的行（first_seq/last_seq 都是 0）经
		// `COUNT(seq < 0) = 0` 天然保持 0，而加一个 `AND last_seq > 0` 之类的守卫会把
		// "first_seq>0 但 last_seq=0"这种畸形行**静默跳过**（它的 first_seq 会留在旧位次上）。
		`UPDATE runs SET
			first_seq = (SELECT COUNT(*) FROM events e WHERE e.session_id = runs.session_id AND e.seq < runs.first_seq),
			last_seq  = (SELECT COUNT(*) FROM events e WHERE e.session_id = runs.session_id AND e.seq < runs.last_seq)`,
		`UPDATE messages SET
			seq_lo = (SELECT COUNT(*) FROM events e WHERE e.session_id = messages.session_id AND e.seq < messages.seq_lo),
			seq_hi = (SELECT COUNT(*) FROM events e WHERE e.session_id = messages.session_id AND e.seq < messages.seq_hi)`,

		// ——— ③ events 重建：分会话重编号（0 起、保持相对顺序）———
		//
		// 列集合与 v17 一致（身份 `id` + 位次 `seq`），**只是位次换义**——所以本步不是"清库重建"，
		// 而是"同表就地重写"（登记在 archtest 的 `v15InPlaceDrops`：行数与 id 逐行保留）。
		//
		// `idx_events_seq`（v17 的全局唯一）随 `DROP TABLE events` 一起消失——**刻意不再显式 DROP**
		// （那条索引属于被删掉的那张表，再写一句会遇到"没有这个索引"，而两句写法还会留下
		// "到底是哪句生效"的疑点）。唯一的位次约束换成 `(session_id, seq)` 复合唯一。
		`CREATE TABLE events_v21 (
			id           TEXT PRIMARY KEY,
			session_id   TEXT NOT NULL REFERENCES sessions(id),
			seq          INTEGER NOT NULL,
			type         TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '',
			created_at   TEXT NOT NULL
		)`,
		`INSERT INTO events_v21 (id, session_id, seq, type, payload_json, created_at)
			SELECT id, session_id,
			       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq) - 1,
			       type, payload_json, created_at
			FROM events`,
		`DROP TABLE events`,
		`ALTER TABLE events_v21 RENAME TO events`,
		`CREATE UNIQUE INDEX idx_events_session ON events(session_id, seq)`,
		`CREATE INDEX idx_events_session_type ON events(session_id, type, seq)`,

		// ——— ④ 计数器回填（每会话已分配到的位次 = 该会话最大 seq）———
		`INSERT INTO event_sequence (session_id, seq)
			SELECT session_id, MAX(seq) FROM events GROUP BY session_id`,

		// ——— ⑤ 会话水位按会话内位次对齐 ———
		`UPDATE sessions SET last_seq =
			COALESCE((SELECT MAX(seq) FROM events e WHERE e.session_id = sessions.id), 0)`,

		// ——— ⑥ 全局计数键退场 + 读模型重建标记 ———
		//
		// 删 `events_last_seq` 在本步是**语义上真的不再需要它**（取号器换成 `event_sequence`）。
		// v17 的注里警告过"别乱清"——那一次清它是**误清**（行照搬却让新事件从 1 起重跳）；
		// 本步与那种情形相反：新号序根本不再读这个键，留着它只会让下一个读 schema 的人
		// 以为全局位次还活着。旧二进制对高版本库**拒绝启动**（v17 已建立这条闸门），
		// 不存在"旧码读新库"。
		`DELETE FROM meta WHERE key = 'events_last_seq'`,

		// `read_models_rebuild` 是给启动重建通道看的标记：值 = 让读模型失效的那一版。
		// `messages` 的口径（白名单投影）在 M3 定稿，本步先把它立起来——重建是**幂等**的，
		// 多跑一次不会把数据带偏（判据 T8：`mate doctor` 的 read-models 检查在重建前后都 0 drift）。
		`INSERT INTO meta (key, value) VALUES ('read_models_rebuild', '21')`,
	},
}
