package store

// 本文件是 **v26**（会话模型引用改平台行 id）的版本步本体（设计
// `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md` §5.4 定案「辰」/§6；计划
// `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md` §4.M3.T1）。
//
// 两件事：
//
//	① `sessions.provider_id` 出列。v19 删掉 agent 面之后这一列**没有任何写入点**
//	   （`SessionRec.ProviderID` 只被读、从不被赋值），线上它恒为空串——一个恒空的列不是"预留"，
//	   它只是让下一个读它的人以为"这里存着会话用的是哪个供应商"。供应商由**模型行**给出
//	   （`models.provider_id`），再存一列就是第二事实源。
//	② 给存量会话的 `model_id` 打一个"待回填"标记：存量装的是**底座的不透明值**
//	   （dsh 下是 `["deepseek-main","gpt-4o"]` 这个 JSON 数组），要变成**平台模型行 id**（`mdl_…`）。
//
// ②为什么只能在应用层做：回填要把"平台行 → 运行时值"过一遍 adapter，而 `internal/persistence/store`
// **不得 import adapter**（守卫 `TestInternalDoesNotImportRuntimeAdapters`）——所以迁移只**置标记**，
// 真正的回填由持注册表的 `manager.backfillSessionModelRefs` 在启动时做（同 `meta.read_models_rebuild`
// 的形状：算不出来就不打勾，下次启动再来）。
//
// **本步一个字都不改 `sessions.model_id`**：迁移里做不了对账，而"改了但没改对"比"没改"更糟
// （见设计 §5.4 的 (甲)/(乙) 两条路，采纳 (乙) 一次性回填）。所以升级之后到回填完成之前，
// 那些会话的 `model_id` 仍是旧形态——回填是幂等的、会在每次启动重试。
//
// 语句里的 `sessions` / `provider_id` / `sessions_model_ref_backfill` 都是**字面量**（v15–v25 同一条
// 纪律：版本步只许写当时那一版的名字，绝不引用 `model.TableName*` 或 Go 字段名）。
var migrationV26 = migration{
	version: 26,
	name:    "会话模型引用改平台行 id：sessions 删 provider_id（v19 后无写入点）+ 置回填标记",
	// 备份：本步删除一列并置一个 meta 标记——`backups/<db>.bak-v26-<ts>` 是唯一的回退窗口
	// （旧二进制对 v26 库拒绝启动），而且**恢复备份 = 连应用层的回填一起撤销**。
	backup: "v26",
	statements: []string{
		// 1. `sessions.provider_id` 出列。
		//
		//    用 `DROP COLUMN` 而不是重建表（与 v19 删 agent_id 同形，理由逐字相同）：本仓已实测
		//    该列上**没有**索引/约束/生成列/视图引用它（sessions 的 4 条索引是
		//    list/origin/fingerprint/workspace，都不含它），SQLite 3.35+ 的 DROP COLUMN 会原子地
		//    重写表定义。
		`ALTER TABLE sessions DROP COLUMN provider_id`,

		// 2. 回填标记（照 `migrate_v23.go` 第 5 条的逐字形态：先 UPDATE 再 INSERT-if-absent）。
		//
		//    先 UPDATE 是为了覆盖"上一次跑到一半"的残留（值可能与本次不同），INSERT-if-absent 是
		//    为了新建（meta 表是 KV，两句话合起来 = upsert，且不依赖 `ON CONFLICT` 的拼写）。
		`UPDATE meta SET value = '26' WHERE key = 'sessions_model_ref_backfill'`,
		`INSERT INTO meta (key, value) SELECT 'sessions_model_ref_backfill', '26'
		   WHERE NOT EXISTS (SELECT 1 FROM meta WHERE key = 'sessions_model_ref_backfill')`,
	},
}
