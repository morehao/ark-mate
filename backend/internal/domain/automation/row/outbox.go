package row

import "time"

// 投递状态（`automation_outbox.status` 的三个取值）。
//
// 词表放在这一层（而不是 `platform/automation` 的 Run* 那族）：那几个词描述的是**一次触发**，
// 这三个描述的是**一次投递**——同一个 occurrence 可以"触发成功、投递还在重试"。
const (
	// OutboxPending 待投递（含"投过、失败了、等下次重试"）。
	OutboxPending = "pending"
	// OutboxSent 已投递（会话已建起来，或已确认不需要再投）。
	OutboxSent = "sent"
	// OutboxFailed 放弃（尝试次数用尽；`last_error` 是最后那次的原因）。
	OutboxFailed = "failed"
)

// OutboxItem 一条待投递的自动化触发（`automation_outbox` 表的行形状，v33 / T5）。
//
// 它记的是**投递入参**，不是运行结果：结果在会话里（ADR-4「不回写会话结果」没有变），
// 这里只保证"这次触发要建哪个会话"这件事**不会因为通道一时不可用而丢掉**。
type OutboxItem struct {
	// ID 自增主键（本表没有对外读面，不需要铸造 id）。
	ID int64
	// AutomationID 触发它的自动化定义 id。
	AutomationID string
	// Occurrence 本次 occurrence 的文本形态（RFC3339Nano(UTC)）；空 = 不定时（手动触发不进 outbox）。
	Occurrence string
	// Trigger 触发来源（scheduled / recovery）。
	Trigger string
	// Payload 投递入参的 JSON（`HostSessionReq` 的直投影：prompt / cwd / model / origin）。
	Payload string
	// IdempotencyKey 幂等键（`automation_id|occurrence|trigger`，库级唯一）：
	// 同一个 occurrence 重复入队只会有一行——这是"补投且只投一次"的第一道闸。
	IdempotencyKey string
	// Status 见 OutboxPending / OutboxSent / OutboxFailed。
	Status string
	// Attempts 已经尝试投递的次数（含失败）。
	Attempts int64
	// NextAttemptAt 下一次允许尝试的时刻（退避；已投递/已放弃的行不再关心它）。
	NextAttemptAt time.Time
	// LastError 最后一次失败的原因（人话，脱敏后的错误串）。
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}
