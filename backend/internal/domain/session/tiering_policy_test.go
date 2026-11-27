package session

import (
	"testing"
	"time"
)

// TestTierBoundaries 边界逐档（设计表：<7天热、7–30天温、30天+冷清理；边界归上档）。
func TestTierBoundaries(t *testing.T) {
	p := DefaultTieringPolicy()
	now := time.Unix(1_700_000_000, 0)
	day := 24 * time.Hour
	cases := []struct {
		idle time.Duration
		want Tier
	}{
		{0, TierHot},
		{day, TierHot},
		{7*day - time.Second, TierHot},
		{7 * day, TierWarm},  // 恰好 7 天 → 温
		{20 * day, TierWarm}, // 7–30 天 → 温
		{30*day - time.Second, TierWarm},
		{30 * day, TierPurge}, // 恰好 30 天 → 冷清理
		{100 * day, TierPurge},
	}
	for _, c := range cases {
		if got := p.Tier(now.Add(-c.idle), time.Time{}, now); got != c.want {
			t.Errorf("idle=%v → %s want %s", c.idle, got, c.want)
		}
	}
}

// TestTierTombstoneAlwaysPurge：已删（tombstone 非零）→ 冷清理，无视活跃时间。
func TestTierTombstoneAlwaysPurge(t *testing.T) {
	p := DefaultTieringPolicy()
	now := time.Unix(1_700_000_000, 0)
	if got := p.Tier(now, now.Add(-time.Hour), now); got != TierPurge {
		t.Fatalf("tombstone 会话应判冷清理，got %s", got)
	}
}

// TestTierZeroLastActiveIsHot：从未记录活跃（零值）→ 保守留热（不误逐）。
func TestTierZeroLastActiveIsHot(t *testing.T) {
	p := DefaultTieringPolicy()
	now := time.Unix(1_700_000_000, 0)
	if got := p.Tier(time.Time{}, time.Time{}, now); got != TierHot {
		t.Fatalf("lastActive 零值应判热（保守），got %s", got)
	}
}

// TestTierClockSkewIsHot：now 早于 lastActive（未来时间/时钟回拨）→ 当作刚活跃 → 热。
func TestTierClockSkewIsHot(t *testing.T) {
	p := DefaultTieringPolicy()
	now := time.Unix(1_700_000_000, 0)
	if got := p.Tier(now.Add(10*24*time.Hour), time.Time{}, now); got != TierHot {
		t.Fatalf("未来 lastActive 应判热，got %s", got)
	}
}

// TestTierCustomThresholds：自定义阈值生效；<=0 回落默认（7/30 天）。
func TestTierCustomThresholds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	day := 24 * time.Hour
	// 1 天转温、3 天转冷。
	p := TieringPolicy{WarmAfter: 1 * day, PurgeAfter: 3 * day}
	if got := p.Tier(now.Add(-2*day), time.Time{}, now); got != TierWarm {
		t.Errorf("自定义阈值 2 天应判温，got %s", got)
	}
	if got := p.Tier(now.Add(-4*day), time.Time{}, now); got != TierPurge {
		t.Errorf("自定义阈值 4 天应判冷清理，got %s", got)
	}
	// 零值阈值回落默认 7/30：8 天 → 温（默认阈值下）。
	z := TieringPolicy{}
	if got := z.Tier(now.Add(-8*day), time.Time{}, now); got != TierWarm {
		t.Errorf("零值阈值应回落默认(7/30)，8 天判温，got %s", got)
	}
}
