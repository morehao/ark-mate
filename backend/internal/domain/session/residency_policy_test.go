package session

import (
	"reflect"
	"testing"
	"time"
)

func ts(sec int) time.Time { return time.Unix(int64(sec), 0) }

// TestResidencyNoEvictUnderCap：驻留数未超上限 → 不驱逐。
func TestResidencyNoEvictUnderCap(t *testing.T) {
	p := ResidencyPolicy{NResident: 3}
	got := p.Evictions([]ResidentSession{
		{ID: "a", LastActive: ts(1)}, {ID: "b", LastActive: ts(2)},
	})
	if len(got) != 0 {
		t.Fatalf("未超上限不该驱逐，got %v", got)
	}
}

// TestResidencyLRUOverIdleOnly：超上限时按 idle 的 LRU（最久未活跃）驱逐到等于上限——M3-1 核心规则。
func TestResidencyLRUOverIdleOnly(t *testing.T) {
	p := ResidencyPolicy{NResident: 2}
	got := p.Evictions([]ResidentSession{
		{ID: "oldest", LastActive: ts(1)},
		{ID: "mid", LastActive: ts(5)},
		{ID: "newest", LastActive: ts(9)},
	})
	// 3 条驻留、上限 2 → 逐 1 条，取最久未活跃的 oldest。
	if !reflect.DeepEqual(got, []string{"oldest"}) {
		t.Fatalf("应驱逐最久未活跃的 oldest，got %v", got)
	}
}

// TestResidencyNeverEvictExecuting（M3-1 验收闸「驱逐不出现在 executing 会话」）：
// executing 会话即使是最久未活跃，也绝不在驱逐候选里；只能逐 idle。
func TestResidencyNeverEvictExecuting(t *testing.T) {
	p := ResidencyPolicy{NResident: 2}
	got := p.Evictions([]ResidentSession{
		{ID: "exec-very-old", Executing: true, LastActive: ts(1)}, // 最久，但在执行 → 不可逐
		{ID: "idle-a", LastActive: ts(5)},
		{ID: "idle-b", LastActive: ts(9)},
	})
	// 3 条驻留、上限 2 → 逐 1；executing 排除后最久 idle 是 idle-a。
	if !reflect.DeepEqual(got, []string{"idle-a"}) {
		t.Fatalf("executing 绝不可驱逐，应逐 idle-a，got %v", got)
	}
}

// TestResidencyPrefersOverCapWhenOnlyExecuting：驻留超限但 idle 不够时，**宁可暂时超限也绝不动
// executing**（C3：失败不可逆，不许 cancel 用户任务）。
func TestResidencyPrefersOverCapWhenOnlyExecuting(t *testing.T) {
	p := ResidencyPolicy{NResident: 1}
	got := p.Evictions([]ResidentSession{
		{ID: "e1", Executing: true, LastActive: ts(1)},
		{ID: "e2", Executing: true, LastActive: ts(2)},
		{ID: "idle-x", LastActive: ts(3)},
	})
	// 3 条驻留、上限 1 → 名义要逐 2，但只剩 1 条 idle → 只逐 idle-x，两条 executing 全留（超限）。
	if !reflect.DeepEqual(got, []string{"idle-x"}) {
		t.Fatalf("idle 不够时只逐 idle、绝不动 executing，got %v", got)
	}
}

// TestResidencyAllExecutingNothingEvicted：全在 executing → 一条都不逐（即使远超上限）。
func TestResidencyAllExecutingNothingEvicted(t *testing.T) {
	p := ResidencyPolicy{NResident: 1}
	got := p.Evictions([]ResidentSession{
		{ID: "e1", Executing: true, LastActive: ts(1)},
		{ID: "e2", Executing: true, LastActive: ts(2)},
		{ID: "e3", Executing: true, LastActive: ts(3)},
	})
	if len(got) != 0 {
		t.Fatalf("全 executing 时不该驱逐任何会话，got %v", got)
	}
}

// TestResidencyUnlimitedWhenNonPositive：N_resident<=0 = 不限制，不驱逐。
func TestResidencyUnlimitedWhenNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		p := ResidencyPolicy{NResident: n}
		got := p.Evictions([]ResidentSession{
			{ID: "a", LastActive: ts(1)}, {ID: "b", LastActive: ts(2)}, {ID: "c", LastActive: ts(3)},
		})
		if len(got) != 0 {
			t.Fatalf("N_resident=%d 应不限制，got %v", n, got)
		}
	}
}

// TestResidencyStableTieBreak：LastActive 相同时按 id 升序驱逐（确定性，便于压测/复现）。
func TestResidencyStableTieBreak(t *testing.T) {
	p := ResidencyPolicy{NResident: 1}
	got := p.Evictions([]ResidentSession{
		{ID: "b", LastActive: ts(5)}, {ID: "a", LastActive: ts(5)},
	})
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("同活跃时间应按 id 升序取 a，got %v", got)
	}
}
