package store

import (
	"context"
	"fmt"
	"regexp"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"gorm.io/gorm"
)

// 本文件是 **v22**（轮次实体化）的版本步本体（设计
// `.dsh/docs/specs/2026-10-14-data-model-v21-design.md` §D2/§D4；计划 §4.M2.T1）。
//
// 三件事，一件都不能少：
//
//	① `runs` 的"每会话每起点一条"从代码约定变成 **DB 不变式**（`UNIQUE(session_id, first_seq)`）；
//	② `runs.id` 从派生串 `r-<session>-<first_seq>` **重铸**成平台铸号 `run_<ULID>`，
//	   并在同一事务里改指 `messages.run_id`（主键重铸 ⇒ 引用必须同事务改写，R7）；
//	③ `inputs` 补会话内位次 `seq`（`UNIQUE(session_id, seq)`，D4）。
//
// 为什么 ② 必须做：`runs.id` 是**派生值**，而它参与身份构造（`r-<session>-<first_seq>`）——
// 于是"窗口从哪里起算"这条规则的任何一次调整都会改掉历史轮次的 id（M2 之前刚为它打过一次
// 症状级止血：轮界裁到首条用户消息）。改成一经铸造就不再变的 `run_<ULID>` 之后，窗口只是
// 行的两列数据（`first_seq`/`last_seq`），规则再变也不会动身份。
//
// 为什么 ② 里的 `id` 不能"加个前缀了事"：`run_<session>-<seq>` 会击穿
// `^run_[0-9A-HJKMNP-TV-Z]{26}$` 这条形状不变式（与 v15 重铸 `models.id` 同一条纪律）。
//
// **本步不伪造事件**（2026-10-14 端到端验收实测后写明）：v22 之前的历史轮**没有** `run.started`
// 事件（那条事件是本步引入的语义），所以升级后这些行的 `first_seq` 指不到任何 `run.started`——
// 老库上这条判据会整片非零，这是**事实**不是漏做。事件流是"当时发生了什么"的记录（J4：身份随
// 数据走），迁移补出来的事件会改 transcript、改 `events` 的身份集合、还会让界面里多出一轮凭空的
// 「开始」。新轮由 `domain/session` 的"开轮三连"保证对称（`Runner.Prompt`）；
// 判据见 `TestMigrateV22DoesNotFabricateRunStarted`。
//
// 语句里的 `runs` / `messages` / `inputs` / `idx_*` 都是**字面量**（守卫⑤：版本步只许写历史
// 字面量，绝不引用 `model.TableName*` 常量）。
var migrationV22 = migration{
	version: 22,
	name:    "轮次实体化：runs 唯一约束 + 主键重铸为 run_<ULID> + inputs 会话内位次",
	// 备份：本步**就地重铸既有行的主键**（`runs.id` 与指向它的 `messages.run_id`）——改写了
	// 既有数据，与 v12/v13/v15/v16/v17/v19/v20/v21 同一档。旧二进制对 v22 库**拒绝启动**
	// （`latestSchemaVersion` 前进），所以备份是唯一的回退窗口，`mate doctor` 会打印它。
	backup: "v22",
	statements: []string{
		// 1. 「每会话每起点一条」成为 DB 不变量（设计 J6）。
		//
		//    它替代的是"用窗口算出来的 id 当幂等键"：那个 `r-<session>-<first_seq>` 只在
		//    「窗口起点规则恰好没变」时才幂等，规则一变，同一个终态事件就会插出第二条行。
		//
		//    存量数据上这条索引**结构上不可能**失败：旧 id 由 (session_id, first_seq) 派生，
		//    同键两行必然同 id、撞主键。真撞上说明库被手工改过——那时应当红，而不是静默跳过。
		`CREATE UNIQUE INDEX idx_runs_session_first_seq ON runs(session_id, first_seq)`,

		// 2. `inputs` 补会话内位次（D4）。
		//
		//    `target_run_id` 从此降为**审计快照**（它今天写的是内存句柄的 `run_<ULID>`，与库里
		//    的 `r-<session>-<seq>` 两套 id 互不相认，作不了投递判据）；"排在哪一轮后面"由
		//    `seq` + `delivery` 表达。回填按入队顺序（created_at，同刻按 id，保证确定性）。
		`ALTER TABLE inputs ADD COLUMN seq INTEGER NOT NULL DEFAULT 0`,
		`UPDATE inputs SET seq = (SELECT COUNT(*) FROM inputs i2
		   WHERE i2.session_id = inputs.session_id
		     AND (i2.created_at < inputs.created_at OR (i2.created_at = inputs.created_at AND i2.id <= inputs.id))) - 1`,
		`CREATE UNIQUE INDEX idx_inputs_session_seq ON inputs(session_id, seq)`,

		// 3. 读模型重建标记（M1 起由 `session.Plane` 的 `rebuildStaleReadModels` 消费；原 `manager.*`）。
		//
		//    M1 只重建 `messages`；本步之后 `runs` 也进了重建面（进行中的轮次在库里**有行**了，
		//    它同样能从 events 重建），所以标记抬到 22。
		`UPDATE meta SET value = '22' WHERE key = 'read_models_rebuild'`,
		`INSERT INTO meta (key, value) SELECT 'read_models_rebuild', '22'
		   WHERE NOT EXISTS (SELECT 1 FROM meta WHERE key = 'read_models_rebuild')`,
	},
	apply: migrateV22,
}

// v22RunIDShape 是重铸后必须成立的形状（`run_` + 26 位 Crockford Base32）。
var v22RunIDShape = regexp.MustCompile(`^run_[0-9A-HJKMNP-TV-Z]{26}$`)

// v22OldRun 是重铸前的一行 run（只取重铸与归属需要的列）。
type v22OldRun struct {
	ID        string
	SessionID string
}

// migrateV22 执行 v22 的 Go 步骤：重铸 `runs.id` + 同事务改写 `messages.run_id` +
// 给 `run_id` 为空的旧 message 行按窗口归属。
//
// 全部走裸 SQL：迁移不经过 DAO/CAS，否则会凭空产生定义审计行（与 v12/v13/v15 的手法一致）。
func migrateV22(ctx context.Context, tx *gorm.DB, _ *Store) error {
	db := tx.WithContext(ctx)

	// ① 读全部 run 行（按会话 + 起点排序，让重铸顺序确定、可复现）。
	var runs []v22OldRun
	if err := db.Raw(`SELECT id, session_id FROM runs ORDER BY session_id, first_seq`).Scan(&runs).Error; err != nil {
		return fmt.Errorf("v22: 读 runs.id: %w", err)
	}

	// ② 逐行重铸 + 改指引用。**顺序**：先把行本身换掉，再改指向它的消息行——反过来会有一瞬
	//    间"消息指向一个不存在的 run"，虽然同一事务里外部看不见，但读这段代码的人不该需要
	//    想清楚这件事。
	remapped := 0
	for _, r := range runs {
		if r.ID == "" {
			return fmt.Errorf("v22: runs 里有一行空 id（session=%s），无法重铸", r.SessionID)
		}
		nw := objid.New(objid.Run)
		if err := db.Exec(`UPDATE runs SET id = ? WHERE id = ?`, nw, r.ID).Error; err != nil {
			return fmt.Errorf("v22: 重铸 runs.id %q: %w", r.ID, err)
		}
		if err := db.Exec(`UPDATE messages SET run_id = ? WHERE run_id = ?`, nw, r.ID).Error; err != nil {
			return fmt.Errorf("v22: 改指 messages.run_id %q: %w", r.ID, err)
		}
		if !v22RunIDShape.MatchString(nw) {
			return fmt.Errorf("v22: 铸出的 run id %q 不匹配 %s", nw, v22RunIDShape)
		}
		remapped++
	}

	// ③ `run_id` 为空的旧 message 行：按 `seq_lo` 落在哪个 run 的窗口里归属（v21 已把窗口列
	//    重编号成会话内位次，所以这里的比较是同口径的）。
	//
	//    窗口不覆盖任何一行的（历史脏数据 / `last_seq` 未收口的行）保持空——**不猜**：给一条
	//    消息安一个错的轮次，比让它暂时没有归属更难发现。这类行由 M3 的整会话重建收口。
	if err := db.Exec(`UPDATE messages SET run_id = COALESCE((
	       SELECT r.id FROM runs r
	        WHERE r.session_id = messages.session_id
	          AND messages.seq_lo >= r.first_seq
	          AND (r.last_seq < 0 OR messages.seq_lo <= r.last_seq)
	        ORDER BY r.first_seq DESC LIMIT 1), run_id)
	    WHERE run_id = ''`).Error; err != nil {
		return fmt.Errorf("v22: 空 run_id 的消息按窗口归属: %w", err)
	}

	// ④ 对账：行数不变、重铸数一致、重铸后 id 形状全对且互不相同。
	var total, shaped, distinct int64
	if err := db.Raw(`SELECT COUNT(*), COUNT(DISTINCT id) FROM runs`).Row().Scan(&total, &distinct); err != nil {
		return fmt.Errorf("v22: 读新 runs 计数: %w", err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM runs WHERE id LIKE 'run_%'`).Scan(&shaped).Error; err != nil {
		return fmt.Errorf("v22: 读重铸后的 runs 计数: %w", err)
	}
	switch {
	case int64(len(runs)) != total || remapped != len(runs):
		return fmt.Errorf("v22: 对账失败：迁移前 %d 行 / 重铸 %d 行 / 迁移后 %d 行", len(runs), remapped, total)
	case shaped != total:
		return fmt.Errorf("v22: 对账失败：%d 行里只有 %d 行是 run_ 铸号（有行没被重铸）", total, shaped)
	case distinct != total:
		return fmt.Errorf("v22: 对账失败：%d 行里只有 %d 支不重复的 id（铸号撞了）", total, distinct)
	}
	return nil
}
