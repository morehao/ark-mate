package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"gorm.io/gorm"
)

// v15TablePlan 登记 v15 **重建**（DROP + CREATE）的每一张表，以及它"有没有元数据要清"。
//
// 为什么要有这张表（R6）：`DROP TABLE` 与"清掉指向旧行的元数据"是**两件必须成对发生**的事，
// 而漏掉后者的表现是静默的——比如 `meta.imported_transcripts` 不清，`mate migrate` 会认为
// "已导入"然后空转，库里却空空如也，没有任何错误码。守卫⑥ 拿它跟 v15 的 `DROP TABLE` 清单
// 逐张比对：少一张、多一张都红。没有元数据要清的表也要**显式写"无"**。
var v15TablePlan = []v15TableEntry{
	// `imported_transcripts` 登记在会话表上：它是「transcript 已导入」的标记，而这条会话面
	// 整体消失了——不清它，`mate migrate` 会认为"已导入"然后空转（§8.4 ①，静默失效）。
	{Table: "conversations", Metadata: "meta.imported_transcripts"},
	// `events` 这一行**曾经**登记 `meta.events_last_seq`（v15 清空 events 后必须清掉那个计数行，
	// 否则新事件从旧高水位起跳）。v21（2026-10-14）把全局号序整体换成每会话一行的
	// `event_sequence`，并在**同一次 Migrate 调用里无条件**删掉那个键（v21 永远排在 v15 之后，
	// 任何走到最新版的库都会经过它），于是本步不再需要清它——那个键在 Migrate 结束时必然不存在。
	//
	// 如实记：这不是"忘了清"，是把清理搬到了唯一还懂那个键的版本步里。判据仍然是
	// `TestDestructiveRebuildIsOneShot` 的第 ④ 段（本表与 DROP 清单一一对应），
	// 只是 `events` 这一行如今没有元数据要清。**历史 DDL 与 v21 的语句都不回改。**
	{Table: "events", Metadata: "无"},
	{Table: "runs", Metadata: "无"},
	{Table: "messages", Metadata: "无"}, // 可从 events 重建，而 events 也在本次清空之列
	{Table: "permission_memories", Metadata: "无"},
	{Table: "inputs", Metadata: "无"},
	{Table: "workspaces", Metadata: "无"}, // 装配期 BootstrapDefaultWorkspace 会在空表时重建默认项
}

// v15TableEntry 一行 = 一张被重建的表 + 它的元数据处置（"无" 或具体 meta 键）。
type v15TableEntry struct {
	Table    string
	Metadata string
}

// v15IDRecastPlan 登记 v15 **不重建但重铸主键**的表，以及它的引用重写清单（R7）。
//
// `models` 是本次唯一的例外（用户 2026-10-11 拍板）：它没有文件来源（providers 有
// providers.json、定义类有定义目录），丢了只能人工重建，所以列集合一个字节不动、只把旧行 id
// 重铸成 `mdl_<ULID>` 并同事务改指**唯一存活的引用**。守卫⑥ 拿它与 v15TablePlan 的并集
// 做一致性断言：重建清单不得包含重铸表（否则就变成"重建时顺手重铸"这种没人想要的双重语义）。
var v15IDRecastPlan = []v15IDRecastEntry{
	{Table: "models", References: []string{"meta." + model.MetaKeyDefaultModel}},
}

// v15IDRecastEntry 一行 = 一张主键被重铸的表 + 必须同事务改写的引用键。
type v15IDRecastEntry struct {
	Table      string
	References []string
}

// migrateV15 是 v15 的 Go 步骤：**只清元数据 + 重铸 models.id**，不做任何行级搬运
// （T4：没有 UPDATE ... SELECT，没有 INSERT ... SELECT；被重建的 7 张表整体清空）。
//
// 全部走裸 SQL：迁移不经过 DAO/CAS，否则会凭空产生 `capability_revisions` 审计行，
// 也会被 revision 语义约束住——与 v12/v13 的手法一致。
func migrateV15(ctx context.Context, tx *gorm.DB, s *Store) error {
	// ① 清 `imported_transcripts`。
	//
	//    不清它 → `mate migrate` 撞到标记直接返回 ErrAlreadyImported，而本迁移刚把 events
	//    清空——用户得到"已导入"而库里空空如也（§8.4 ①，静默失效）。
	//
	//    刻意清掉而**不是**保留 `imported_providers`（providers 表没重建，标记仍有效）与
	//    `runtime_config_digest`（模型清空后期望集自然不同，下次同步会重新生成）。
	//
	//    同一句里**曾经**还清 `events_last_seq`（v15 清空 events 后必须把计数行归零）。v21 起
	//    那个键由 v21 步骤无条件删除，本步不再持有它——见 `v15TablePlan` 里 `events` 一行的注。
	if err := tx.WithContext(ctx).Exec(
		`DELETE FROM meta WHERE key = ?`,
		model.MetaKeyImportMarker,
	).Error; err != nil {
		return fmt.Errorf("清 meta.imported_transcripts: %w", err)
	}

	// ② 清 `automations` 的 5 个运行态列——它们指向已经消失的会话。
	//
	//    **`last_fired_at` 刻意保留**（ADR-10）：它是"这个 occurrence 认领过没有"的唯一判据，
	//    清掉它会让已经跑过的 `once` 任务在重启后再跑一次（有副作用的 prompt 重复执行）。
	if err := tx.WithContext(ctx).Exec(
		`UPDATE automations
		    SET last_session_id = '', last_status = '', last_reason = '',
		        recent_runs = '[]', consecutive_failures = 0`,
	).Error; err != nil {
		return fmt.Errorf("清 automations 运行态列: %w", err)
	}

	// ③ 重铸 `models.id` 并同事务改指 `meta.default_model`（R7）。
	return recastV15ModelIDs(ctx, tx)
}

// recastV15ModelIDs 把 `models.id` 逐行换成 `mdl_<ULID>`，并把 `meta.default_model`
// 改指新 id。
//
// 为什么**不是**"给旧 id 加个 `mdl_` 前缀"：那会留下 `mdl_<旧 uuid>` 这种 40 字符的形状，
// 击穿 `^mdl_[0-9A-HJKMNP-TV-Z]{26}$` 这条形状不变式（而它是守卫③ 的判据）。
//
// 唯一性不受影响：`(provider_id, model_id)` 的唯一索引与 `idx_models_provider` 都不动，
// 行数与内容列逐行不变——变的只有 id 列。
func recastV15ModelIDs(ctx context.Context, tx *gorm.DB) error {
	var ids []string
	if err := tx.WithContext(ctx).Raw(`SELECT id FROM models ORDER BY id`).Scan(&ids).Error; err != nil {
		return fmt.Errorf("读取 models.id: %w", err)
	}
	remap := make(map[string]string, len(ids))
	for _, old := range ids {
		if old == "" {
			return errors.New("models 里有一行空 id，无法重铸")
		}
		nw := objid.New(objid.Model)
		if err := tx.WithContext(ctx).Exec(`UPDATE models SET id = ? WHERE id = ?`, nw, old).Error; err != nil {
			return fmt.Errorf("重铸 models.id %q: %w", old, err)
		}
		remap[old] = nw
	}
	if len(remap) == 0 {
		return nil
	}

	// 契约：主键重铸 ⇒ 引用必须同事务改写（R7）。`meta.default_model` 是**唯一存活**的
	// 平台模型 id 引用（已核实：agents.default_model 是只读派生投影、sessions.model 随
	// sessions 重建、ModelCreateReq 没有 id 字段）。
	cur, ok, err := readMetaTx(ctx, tx, model.MetaKeyDefaultModel)
	if err != nil {
		return err
	}
	if !ok || cur == "" {
		return nil
	}
	nw, hit := remap[cur]
	if !hit {
		// 悬空值（指向一条不存在的模型行）：本迁移不"顺手修"它——那不是本次变更的语义，
		// 而且把"用户设过默认"静默抹掉正是最不该做的事。
		return nil
	}
	return writeMetaTx(ctx, tx, model.MetaKeyDefaultModel, nw)
}

// readMetaTx 在事务内读一个 meta 键（不存在 → ("", false, nil)）。
func readMetaTx(ctx context.Context, tx *gorm.DB, key string) (string, bool, error) {
	var value string
	row := tx.WithContext(ctx).Raw(`SELECT value FROM meta WHERE key = ?`, key).Row()
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读 meta.%s: %w", key, err)
	}
	return value, true, nil
}
