package store

// 本文件是 **v23**（消息白名单投影）的版本步本体（设计
// `.dsh/docs/specs/2026-10-14-data-model-v21-design.md` §D3；计划 §4.M3.T1）。
//
// 三件事：
//
//	① `messages` 补 `ordinal`（会话内第几条，1 起、稠密、唯一）——它是给人看的稳定序号，
//	   也是"这一会话的消息投影是不是完整的"这条不变式的载体（`UNIQUE(session_id, ordinal)`）；
//	② 补 `kind`（投影行的种类；当前**恒为空串**，只留扩展位——"只有将来真要多一种可搜事实时
//	   才取值"，J5：一列一义，不留"以后可能有用"的半个含义）；
//	③ 索引重排：`(session_id, run_id)` 那条是为"按轮删改"建的，而 v22 起 `runs` 是实体、
//	   重建走**整会话**（E4），所以 `run_id` 自己成索引（孤儿检查/按轮查），
//	   `(session_id, seq_lo)` 负责"按位次读一整个会话"。
//
// 为什么 `ordinal` 现在必须落库（而不是继续"读出来的时候按 seq_lo 排一下"）：前端要的是
// "第几条消息"这个**稳定**的说法，而位次在 v21 换过一次口径（全局 → 会话内）——按位次排序
// 得到的顺序在口径迁移前后可能是两套。序号一旦落库，读模型重建会把整会话的 ordinal 重铺成
// 1..N（`UNIQUE` 会咬住任何重号），于是"投影是否完整/是否重复"从"看起来对"变成可判定。
//
// 存量行的 ordinal 回填是**确定**的映射（不是启发式）：会话内按 `seq_lo` 升序、同 `seq_lo`
// 按 `id`（ULID 字典序 = 铸造序）——`COUNT(*)` 含自己，于是第一个是 1（判据
// `migrate_v23_test.go`：把 `- 1` 加回去，连续性断言当场红）。
//
// 语句里的 `messages` / `ordinal` / `kind` / `idx_*` 都是**字面量**（守卫⑤：版本步只许写
// 历史字面量，绝不引用 `model.TableName*` 常量）。
var migrationV23 = migration{
	version: 23,
	name:    "消息白名单投影：messages 补 ordinal/kind + 索引重排（会话内序号唯一）",
	// 备份：本步**就地改写既有行的序号**（给全部现存 messages 回填 ordinal）——改写了既有数据，
	// 与 v12/v13/v15/v16/v17/v19/v20/v21/v22 同一档。旧二进制对 v23 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口，`mate doctor` 会打印它。
	backup: "v23",
	statements: []string{
		// 1. 两列都是 `NOT NULL DEFAULT`：加列不需要重建表（`messages` 的列集合是**封闭**的，
		//    A1／`TestIdentifiersFollowLexicalForm` 判据读的就是真实 schema，重建反而把
		//    "当时那一版"的列集合一起改写）。
		`ALTER TABLE messages ADD COLUMN ordinal INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,

		// 2. 回填会话内序号（1 起、稠密）。用关联子查询而不是窗口函数：`UPDATE` 里写不了
		//    `ROW_NUMBER()`，而这里的映射是确定的（`seq_lo` 会话内单调 ⇒ 序号 = 排在自己前面
		//    的同会话行数 + 1）。
		`UPDATE messages SET ordinal = (SELECT COUNT(*) FROM messages m2
		   WHERE m2.session_id = messages.session_id
		     AND (m2.seq_lo < messages.seq_lo OR (m2.seq_lo = messages.seq_lo AND m2.id <= messages.id)))`,

		// 3. 「一会话一序号」成为 DB 不变式（J6）。回填刚好重号时这条索引会红——那时应当红：
		//    重号意味着"两条消息是同一条"，而序号是给人看的，猜不得。
		`CREATE UNIQUE INDEX idx_messages_session_ordinal ON messages(session_id, ordinal)`,

		// 4. 索引重排（旧那条是 `(session_id, run_id)`，承载的两种查询现在各归各的索引）。
		`CREATE INDEX idx_messages_run ON messages(run_id)`,
		`DROP INDEX idx_messages_session`,
		`CREATE INDEX idx_messages_session ON messages(session_id, seq_lo)`,

		// 5. 读模型重建标记（M1 起由 `manager.rebuildStaleReadModels` 消费）：整会话重铺 messages
		//    之后 `ordinal` 才真正稠密（本步的回填只对"当时库里那些行"成立）。
		`UPDATE meta SET value = '23' WHERE key = 'read_models_rebuild'`,
		`INSERT INTO meta (key, value) SELECT 'read_models_rebuild', '23'
		   WHERE NOT EXISTS (SELECT 1 FROM meta WHERE key = 'read_models_rebuild')`,
	},
}
