package session

import "time"

// Tier 是会话日志在**本地卷分层**中的所在层（SaaS 池化 M3-3 / 设计「存储设计」表的 热/温/冷）。
//
// 本层只回答「这条会话的日志**现在该在哪一层**」，不执行搬迁（搬迁由控制面经 golib storage 本地驱动做，
// 是部署期的事）。判决保持纯函数：同输入同输出、可无 IO 地测死。
type Tier string

const (
	// TierHot 热层：正在跑 + 最近活跃（默认），日志在 DSH JSONL 直写的本地 SSD 卷。
	TierHot Tier = "hot"
	// TierWarm 温层：7–30 天未活跃，日志 relocate 到同机归档段（仍 zstd，只挪位置）。
	TierWarm Tier = "warm"
	// TierPurge 冷清理：30 天+ 未访问 / 已删（tombstone），到合规期后按策略本地删除。
	TierPurge Tier = "purge"
)

// TieringPolicy 是「按最近活跃时间判层」的纯判决（M3-3）。
//
// 阈值取设计「存储设计」表：温 = 7–30 天未活跃；冷清理 = 30 天+（或已删）。阈值做成字段（而非硬编码
// 常量）是为了让部署按容量调整——但**默认值**与设计表一致（见 DefaultTieringPolicy）。
type TieringPolicy struct {
	// WarmAfter 超过这么久未活跃 → 温层。<=0 用默认 7 天。
	WarmAfter time.Duration
	// PurgeAfter 超过这么久未活跃 → 冷清理。<=0 用默认 30 天。
	PurgeAfter time.Duration
}

// DefaultTieringPolicy 设计表默认阈值：7 天转温、30 天转冷清理。
func DefaultTieringPolicy() TieringPolicy {
	return TieringPolicy{WarmAfter: 7 * 24 * time.Hour, PurgeAfter: 30 * 24 * time.Hour}
}

// Tier 判定某会话当前应处的层。
//
// 规则（逐条对设计表）：
//  1. **已删（tombstoneAt 非零）** → 冷清理（删除是最终态，不看活跃时间）。
//  2. 否则按 now - lastActive 的时长分档：< WarmAfter → 热；[WarmAfter, PurgeAfter) → 温；
//     >= PurgeAfter → 冷清理。
//  3. 时间处理 fail-safe：
//     - lastActive 为零值（从未记录）= 无法判定活跃 → 保守判**热**（不误逐从未活跃但也可能有用的会话）；
//     - now 早于 lastActive（时钟回拨/未来时间）→ 视为「刚刚活跃」→ **热**。
//
// 刻意不用 `<=` 边界含混：边界归属写死——恰好 WarmAfter 归温、恰好 PurgeAfter 归冷清理。
func (p TieringPolicy) Tier(lastActive, tombstoneAt, now time.Time) Tier {
	if !tombstoneAt.IsZero() {
		return TierPurge
	}
	warmAfter, purgeAfter := p.WarmAfter, p.PurgeAfter
	if warmAfter <= 0 {
		warmAfter = 7 * 24 * time.Hour
	}
	if purgeAfter <= 0 {
		purgeAfter = 30 * 24 * time.Hour
	}
	if lastActive.IsZero() {
		return TierHot // 未记录活跃：保守留热
	}
	idle := now.Sub(lastActive)
	if idle < 0 {
		return TierHot // 时钟回拨/未来时间：当作刚活跃
	}
	switch {
	case idle >= purgeAfter:
		return TierPurge
	case idle >= warmAfter:
		return TierWarm
	default:
		return TierHot
	}
}
