package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameAutomation 定时任务：**定义列 + 运行态列**（M6-7，Plan §7 P-10）。
	TableNameAutomation = "automations"
)

// AutomationEntity 是一条定时任务（M6-7）。
//
// **定义与运行态分列**（Plan §7 P-10）：`Body` 只放用户写的定义（含 `state`——它刻意不在
// `automation.ReservedKeys` 里，见那里的注释：启用/停用/归档本来就是用户操作），运行态
// （`last_fired_at` / `last_status` / … / `recent_runs`）走列。于是"用户往 body 里塞运行态键"
// 这件事**结构上不可能**，而拒写名单（`IsReservedKey` → 40001）仍然保留——否则用户会写出一个
// "我写了它为什么不生效"的定义。
//
// **运行态列不参与 revision CAS**：`Revision`/`UpdatedAt` 只记定义（含 `state`，它是定义键）
// 的提交。调度器每次推进只改运行态列 → 面板上的定义不会看起来"被别人改过"（文件载体里这一步
// 会抬高 mtime；DB 载体把两件事分开之后，只有定义被真改过才撞 40901）。唯一例外是**首次**
// 推进要往正文里补一个 `state`（用户没写过它），那一次会前进一版——之后逐字节稳定。
//
// `recent_runs` 存 JSON 文本（有界，`automation.RecentRunsMax`）：搬进 `events` 表会撞
// `events.session_id NOT NULL REFERENCES sessions`，而 duplicate/overlap/skipped
// 三类结果**没有会话可挂**（Plan §7 P-11）。
type AutomationEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 用户写的定义（原始 JSON，含未知键；**不含**运行态键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次**定义**提交的时刻（RFC3339Nano(UTC)；运行态写回不刷新它）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// LastFiredAt 最近一次被认领的 occurrence（RFC3339Nano(UTC)；空 = 从未触发）。
	//
	// 它是"这个 occurrence 是否已经触发过"的**唯一判据**（ADR-10），也是调度推进的 CAS 判据：
	// 认领 = `WHERE id=? AND last_fired_at=<读到的值>`，0 行 = 别人先认领了。
	LastFiredAt string `gorm:"column:last_fired_at;not null;default:''"`
	// LastStatus 最近一次结果（completed/failed/interrupted/skipped）。
	LastStatus string `gorm:"column:last_status;not null;default:''"`
	// LastReason 最近一次没跑成/失败的原因（人话）。
	LastReason string `gorm:"column:last_reason;not null;default:''"`
	// LastSessionID 最近一次触发产生的会话（overlap 判据用它查会话是否还在跑）。
	LastSessionID string `gorm:"column:last_session_id;not null;default:''"`
	// ConsecutiveFailures 连续失败次数（跑成一次即清零）。
	ConsecutiveFailures int64 `gorm:"column:consecutive_failures;not null;default:0"`
	// RecentRuns 最近若干次触发的留痕（JSON 数组文本，最新在前，有界）。
	RecentRuns string `gorm:"column:recent_runs;not null;default:'[]'"`
}

// TableName 实现 gormdao.Entity。
func (AutomationEntity) TableName() string { return TableNameAutomation }

// AutomationEntityList 是 AutomationEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AutomationEntityList []AutomationEntity
