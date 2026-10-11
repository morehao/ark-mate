package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是定时任务事实源（M6-7）的门面。与能力/agent 面**共用一个 CAS 实现**
// （`dao.DefinitionDao`），但多两类方法，因为 automation 的载体里住着两种东西：
//
//	定义（body + revision）——用户写面，CAS + 审计（复用 capability_revisions，kind=automation）；
//	运行态（last_* / consecutive_failures / recent_runs）——**平台**写，不参与定义 revision，
//	也不进审计（它不是"谁改了定义"，而是一次调度的推进）。
//
// 认领（claim）是这里的核心不变量：`last_fired_at` 就是"这个 occurrence 是否已经触发过"的
// 唯一判据（ADR-10），所以推进它必须带 CAS（`WHERE last_fired_at = <读到的值>`）——0 行就是
// "别人先认领了"，调用方据此**不重复触发**（Plan §7 P-12 的落点）。

// AutomationRuntimeState 一条定时任务的运行态（**列**；`state` 不在其中——它属于定义，见 P-10）。
//
// 时间一律 **RFC3339Nano(UTC) 文本**（空串 = 未触发过）：裸 SQL 入参不经过 serializer，
// 这个结构体就是"运行态列"的边界，字符串化在 manager 完成、在这里原样落库。
type AutomationRuntimeState struct {
	// LastFiredAt 最近一次被认领的 occurrence（空 = 从未触发）。
	LastFiredAt string
	// LastStatus 最近一次结果（completed/failed/interrupted/skipped）。
	LastStatus string
	// LastReason 最近一次没跑成/失败的原因（人话）。
	LastReason string
	// LastSessionID 最近一次触发产生的会话 id。
	LastSessionID string
	// ConsecutiveFailures 连续失败次数。
	ConsecutiveFailures int64
	// RecentRuns 最近若干次触发的留痕（JSON 数组文本；空 = 不改这一列）。
	//
	// 空 = 不改（而不是"清空"）：调用方只推进 `last_fired_at` 时不该顺手把运行史抹掉。
	RecentRuns string
}

// AutomationOverlay 一次运行态写回（可能同时改定义里的 `state`，见 P-10 的说明）。
type AutomationOverlay struct {
	// Body 非空 = 同时覆盖定义正文（平台写 `state` 这类**非保留键**时用）。
	Body string
	// ExpectedRev 仅当 Body 非空时有意义（body 的 CAS 判据）。
	ExpectedRev int64
	// ExpectedLastFired 认领 CAS 判据（"这个 occurrence 还没被别人认领"）。
	ExpectedLastFired string
	// Runtime 要写入的运行态列。
	Runtime AutomationRuntimeState
}

// UpsertAutomationDefinition 提交一次定义写入（新建或覆盖）+ 一条审计，同事务。
func (s *Store) UpsertAutomationDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindAutomation, id, actor, action, bodyDigest(body), expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.automations.WithTx(tx).CreateCAS(ctx, &model.AutomationEntity{
					ID: id, Body: body, Revision: 1, UpdatedAt: now, RecentRuns: "[]",
				})
			}
			return s.automations.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteAutomationDefinition 删除一条定义 + 一条审计，同事务（语义同 DeleteSkillDefinition）。
func (s *Store) DeleteAutomationDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindAutomation, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.automations.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetAutomationDefinition 读一条定义（不存在 → (nil, false, nil)）。
func (s *Store) GetAutomationDefinition(ctx context.Context, id string) (*model.AutomationEntity, bool, error) {
	return s.automations.Get(ctx, id)
}

// ListAutomationDefinitions 列出全部定义（id 升序）。
func (s *Store) ListAutomationDefinitions(ctx context.Context) (model.AutomationEntityList, error) {
	return s.automations.List(ctx)
}

// CountAutomationDefinitions 返回定义行数。
func (s *Store) CountAutomationDefinitions(ctx context.Context) (int64, error) {
	return s.automations.Count(ctx)
}

// ClaimAutomationOccurrence 认领一个 occurrence：把 `last_fired_at` 推进到 `claimedFor`，
// **仅当它仍等于 `expectedLastFired`**。
//
// 这是"不重复触发"的唯一仲裁者：返回 false = 别人（另一个 tick / 另一个进程）先认领了，
// 调用方**必须**放弃这次执行。`body`/`revision` 一概不碰（认领不是定义变更）。
func (s *Store) ClaimAutomationOccurrence(ctx context.Context, id, expectedLastFired, claimedFor string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&model.AutomationEntity{}).
		Where("id = ? AND last_fired_at = ?", id, expectedLastFired).
		Updates(map[string]any{"last_fired_at": claimedFor})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SaveAutomationOverlay 写回运行态（必要时连定义正文一起），单事务、带两个 CAS 判据。
//
//   - `expectedLastFired`：认领判据。0 行 = 这一版运行态基于的认领已被别人推进 →
//     返回 false，调用方**不推进内存态**、下一 tick 再说（P-12）；
//   - `ExpectedRev`（仅当 Body 非空）：正文判据。0 行 = 用户在读取与写回之间改了定义 →
//     返回 false（平台这次运行态写回放弃，与文件载体撞 mtime 的表现一致）。
//
// 返回 false 时**一个字节都没写**（单事务）。
func (s *Store) SaveAutomationOverlay(ctx context.Context, id string, ov AutomationOverlay) (bool, error) {
	updates := map[string]any{
		"last_status":          ov.Runtime.LastStatus,
		"last_reason":          ov.Runtime.LastReason,
		"last_session_id":      ov.Runtime.LastSessionID,
		"consecutive_failures": ov.Runtime.ConsecutiveFailures,
		"last_fired_at":        ov.Runtime.LastFiredAt,
	}
	if ov.Runtime.RecentRuns != "" {
		updates["recent_runs"] = ov.Runtime.RecentRuns
	}
	if ov.Body != "" {
		updates["body"] = ov.Body
		updates["revision"] = ov.ExpectedRev + 1
		updates["updated_at"] = model.FormatTimeOrNow(time.Now())
	}
	q := s.db.WithContext(ctx).Model(&model.AutomationEntity{}).
		Where("id = ? AND last_fired_at = ?", id, ov.ExpectedLastFired)
	if ov.Body != "" {
		q = q.Where("revision = ?", ov.ExpectedRev)
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SaveAutomationRuntime 只写运行态列（认领判据同上）；等价于 `Body == ""` 的
// `SaveAutomationOverlay`，给调用方一个不需要想"要不要带正文"的入口。
func (s *Store) SaveAutomationRuntime(ctx context.Context, id string, expectedLastFired string, rt AutomationRuntimeState) (bool, error) {
	return s.SaveAutomationOverlay(ctx, id, AutomationOverlay{ExpectedLastFired: expectedLastFired, Runtime: rt})
}
