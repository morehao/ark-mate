package automation

import "time"

// ClaimStatus 认领一个 occurrence 的三种结果（对齐 openwork `ports.ts:22-25`）。
type ClaimStatus string

const (
	ClaimClaimed   ClaimStatus = "claimed"   // 无人认领过、且没有在跑 → 执行
	ClaimDuplicate ClaimStatus = "duplicate" // 该 `scheduledFor` 已被认领过 → 跳过（进程重启后重复 tick 的唯一防线）
	ClaimOverlap   ClaimStatus = "overlap"   // 上一次触发产生的会话仍在跑 → 记 skipped，**不排队**
)

// ClaimResult 认领判定结果（`Reason` 是要给用户看的人话，会进 `automation.run` 事件载荷）。
type ClaimResult struct {
	Status ClaimStatus
	Reason string
}

// Decide 判定一次到期触发该怎么做——**纯函数**：时钟、注册表、会话状态都由调用方查好后传进来。
//
// 两个判据的先后顺序是有意的：`duplicate` 比 `overlap` 更硬（"这个时刻已经跑过了"是历史事实，
// 而"有会话在跑"是当前状态）。若反过来，一条正在跑、且同一时刻已被认领过的 automation
// 会被报成 overlap，运行史里就会少一次"其实什么都没做"的记录。
//
// `overlap` **不排队**：短间隔任务排在长任务后面会堆成雪崩，而用户看到的是"一堆会话同时醒来"。
// 记 skipped 至少让人在运行史里看见"这几次因为上一轮没结束而跳过了"（Gate M4 的"skipped 可见"）。
func Decide(scheduledFor time.Time, lastFiredAt *time.Time, hasActiveRun bool) ClaimResult {
	if lastFiredAt != nil && !lastFiredAt.Before(scheduledFor) {
		return ClaimResult{
			Status: ClaimDuplicate,
			Reason: "该时刻已认领过（last_fired_at = " + lastFiredAt.Format(time.RFC3339) + "）",
		}
	}
	if hasActiveRun {
		return ClaimResult{
			Status: ClaimOverlap,
			Reason: "上一次触发产生的会话仍在进行中：本次记 skipped，不排队（排队会让短间隔任务堆积成雪崩）",
		}
	}
	return ClaimResult{Status: ClaimClaimed, Reason: "认领成功"}
}
