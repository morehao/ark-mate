package store

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"gorm.io/gorm"
)

// 本文件是 **v17 的数据迁移**（设计 `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`）。
//
// 它把 `events` 的身份从「seq 就是身份」改成「id 是身份、seq 是位次」，全在同一事务里
// （失败整体回滚，不存在"迁移了一半"）：
//
//	① 读旧 `events` 的行数与最大 seq（对账基准）；
//	② **keyset 分页**（`WHERE seq > ? ORDER BY seq LIMIT n`）逐行铸 `evt_<26 位 ULID>` 灌进
//	   `events_v17`：铸号顺序 = seq 升序，于是 `ORDER BY id ASC` 与 `ORDER BY seq ASC` 同序，
//	   而**唯一的游标仍然是 seq**；
//	③ 逐项对账（行数 / 最大 seq / id 互不相同），任一不成立即返回 error 整体回滚；
//	④ DROP 旧表 → RENAME → 建 2 条索引（含 `idx_events_seq` 的 UNIQUE）。
//
// 为什么用**分页**而不是一条 `INSERT INTO … SELECT`（v16 的 inputs 重建那样）：`id` 必须逐行
// 铸（SQL 里没有"平台铸号"这件事），而且 `events` 是一帧一行——它是本仓第一个 O(全部事件行)
// 的迁移步骤，量级比"几十条定义 / 个位数排队输入"高好几个数量级，一次性读进内存不合适。
//
// 为什么 `meta.events_last_seq` **不动**（与 v15 相反，极易照抄错）：v15 重建 events 时行被清空，
// 所以那行 meta 必须清掉；v17 行**一行不丢**，清掉它会让下一条新事件从 1 重新起跳、与既有行
// 撞号（`idx_events_seq` 的 UNIQUE 会当场把迁移判红）。seq 的权威分配链与 v15 逐字相同。
//
// 语句里的 `events` / `events_v17` / `idx_events_session` 都是**字面量**（守卫⑤：版本步只许写
// 历史字面量，绝不引用 `model.TableName*` 常量）。

// v17EventPageSize 是重建 events 时的 keyset 页大小（一次读多少行进内存）。
const v17EventPageSize = 2000

// v17OldEvent 是旧 events 表的一行（只取搬运需要的列）。
type v17OldEvent struct {
	seq         int64
	sessionID   string
	typ         string
	payloadJSON string
	createdAt   string
}

// migrateV17 执行 v17 的就地重建 + 对账 + 换表。
func migrateV17(ctx context.Context, tx *gorm.DB, _ *Store) error {
	db := tx.WithContext(ctx)

	// ① 对账基准：旧表的行数与最大 seq。
	var beforeCount, beforeMax int64
	if err := db.Raw(`SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM events`).
		Row().Scan(&beforeCount, &beforeMax); err != nil {
		return fmt.Errorf("v17: 读旧 events 计数: %w", err)
	}

	// ② keyset 分页搬运 + 逐行铸号。
	var (
		moved     int64
		copiedMax int64
	)
	after := int64(0)
	for {
		rows, err := db.Raw(
			`SELECT seq, session_id, type, payload_json, created_at
			 FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, v17EventPageSize).Rows()
		if err != nil {
			return fmt.Errorf("v17: 读 events 分页（after=%d）: %w", after, err)
		}
		page := make([]v17OldEvent, 0, v17EventPageSize)
		for rows.Next() {
			var e v17OldEvent
			if serr := rows.Scan(&e.seq, &e.sessionID, &e.typ, &e.payloadJSON, &e.createdAt); serr != nil {
				_ = rows.Close()
				return fmt.Errorf("v17: 扫 events 行: %w", serr)
			}
			page = append(page, e)
		}
		if rerr := rows.Err(); rerr != nil {
			_ = rows.Close()
			return fmt.Errorf("v17: 遍历 events 分页: %w", rerr)
		}
		if cerr := rows.Close(); cerr != nil {
			return fmt.Errorf("v17: 关闭 events 分页游标: %w", cerr)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			id := objid.New(objid.Event)
			if err := db.Exec(
				`INSERT INTO events_v17 (id, seq, session_id, type, payload_json, created_at)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				id, e.seq, e.sessionID, e.typ, e.payloadJSON, e.createdAt).Error; err != nil {
				return fmt.Errorf("v17: 灌入事件 seq=%d: %w", e.seq, err)
			}
			moved++
			if e.seq > copiedMax {
				copiedMax = e.seq
			}
			after = e.seq
		}
	}

	// ③ 对账：行数、最大 seq、身份互不相同。
	var afterCount, afterMax, distinctIDs int64
	if err := db.Raw(`SELECT COUNT(*), COALESCE(MAX(seq), 0), COUNT(DISTINCT id) FROM events_v17`).
		Row().Scan(&afterCount, &afterMax, &distinctIDs); err != nil {
		return fmt.Errorf("v17: 读新 events 计数: %w", err)
	}
	switch {
	case afterCount != beforeCount || afterCount != moved:
		return fmt.Errorf("v17: 对账失败：旧表 %d 行 / 搬了 %d 行 / 新表 %d 行", beforeCount, moved, afterCount)
	case afterMax != beforeMax || afterMax != copiedMax:
		return fmt.Errorf("v17: 对账失败：旧表最大 seq %d / 搬运过 %d / 新表 %d", beforeMax, copiedMax, afterMax)
	case distinctIDs != afterCount:
		return fmt.Errorf("v17: 对账失败：%d 行里只有 %d 支不重复的 id（铸号撞了）", afterCount, distinctIDs)
	}

	// ④ 换表：DROP 旧 → RENAME → 建索引（库级表达 seq 唯一）。
	for _, stmt := range []string{
		`DROP TABLE events`,
		`ALTER TABLE events_v17 RENAME TO events`,
		`CREATE INDEX idx_events_session ON events(session_id, seq)`,
		`CREATE UNIQUE INDEX idx_events_seq ON events(seq)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("v17: 换表步骤 %q: %w", stmt, err)
		}
	}
	return nil
}
