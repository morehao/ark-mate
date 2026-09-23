package automation

import (
	"fmt"
	"time"
)

// 墙钟扫描窗口：daily/weekly 的最远下次时刻一定落在 +7 天内，留一天余量（时区数据库里
// 存在整日被跳过的历史事件，如 2011 年萨摩亚跳掉 12-30），所以两侧各扫 8 天。
const scanDays = 8

// WallFormat Occurrence.Wall 的格式（本地墙钟 + 时区缩写）。
const WallFormat = "2006-01-02 15:04 MST"

// NextAfter 严格晚于 t 的下一次到期。`ok=false` 表示**没有下一次**（`once` 已过或已跑完）。
func (s Schedule) NextAfter(t time.Time) (Occurrence, bool, error) {
	loc, err := s.Location()
	if err != nil {
		return Occurrence{}, false, err
	}
	if s.Kind == KindOnce {
		return s.onceAt(loc, func(at time.Time) bool { return at.After(t) })
	}
	// 按天推进：从 t 的本地日期起扫 scanDays+1 天，取第一个严格晚于 t 的候选。
	return s.firstMatching(t, loc, func(at time.Time) bool { return at.After(t) })
}

// LatestAtOrBefore 不晚于 t 的**最近一次**到期。停机跨了多个 occurrence 时，这里给出的是
// 最后一次（ADR-9：只补最近一次，更早的积压不重放）。
func (s Schedule) LatestAtOrBefore(t time.Time) (Occurrence, bool, error) {
	loc, err := s.Location()
	if err != nil {
		return Occurrence{}, false, err
	}
	if s.Kind == KindOnce {
		return s.onceAt(loc, func(at time.Time) bool { return !at.After(t) })
	}
	// 从 t 的本地日期往前扫：取第一个 <= t 的候选。
	var found Occurrence
	ok := false
	base := startOfLocalDay(t, loc)
	for i := 0; i <= scanDays; i++ {
		d := base.AddDate(0, 0, -i)
		for _, c := range s.dayCandidates(d, loc) {
			if c.At.After(t) {
				continue
			}
			// 同一天内可能有多个候选（理论上是 1 个；DST 回拨也不产生两个候选，
			// 因为候选按墙钟唯一）。取最晚的那个。
			if !ok || c.At.After(found.At) {
				found, ok = c, true
			}
		}
		if ok {
			return found, true, nil
		}
	}
	return Occurrence{}, false, nil
}

// NextDue 下一次**该处理**的到期时刻——调度循环唯一的入口。
//
// 语义（同时覆盖正常运行与停机补跑，故不需要两条代码路径）：
//
//	ref := lastFiredAt（从未跑过则 now）
//	next := NextAfter(ref)
//	next 仍在未来 → 返回 next（尚未到期）
//	next 已过去   → 返回 LatestAtOrBefore(now)，即"最近一次错过的"
//
// 于是：正常运行晚一个 tick 触发 → 返回当次；停机三天 → 只返回最近一次（今天的），
// 不会连跑三次（ADR-9）；`once` 已过期且从未跑过 → 返回那一次（由调用方决定补还是置 inactive，
// 见 Startup）。
func (s Schedule) NextDue(lastFiredAt *time.Time, now time.Time) (Occurrence, bool, error) {
	ref := now
	if lastFiredAt != nil {
		ref = *lastFiredAt
	}
	next, ok, err := s.NextAfter(ref)
	if err != nil {
		return Occurrence{}, false, err
	}
	if ok && next.At.After(now) {
		return next, true, nil
	}
	// 「没有下一次」与「下一次已经过去」在这里合流：都去看最近一次错过的。
	// 两者必须合流，否则 `once` 到期未跑会既拿不到下一次、又拿不到那一次（它已经"过去"了）。
	latest, ok2, err := s.LatestAtOrBefore(now)
	if err != nil || !ok2 {
		return Occurrence{}, false, err
	}
	if lastFiredAt != nil && !latest.At.After(*lastFiredAt) {
		return Occurrence{}, false, nil // 这次已经跑过了
	}
	return latest, true, nil
}

// StartupAction 启动时对一条 automation 的动作（ADR-9 的落点）。
type StartupAction string

const (
	StartupWait     StartupAction = "wait"     // 未到期：按 next_due 正常排队
	StartupRecover  StartupAction = "recover"  // 停机期间错过：补最近一次（trigger=recovery）
	StartupExpire   StartupAction = "expire"   // once 已过期且从未跑过：置 inactive，**不补**
	StartupInactive StartupAction = "inactive" // 已跑完的 once / 已归档：不参与调度
)

// Startup 计算启动时的动作。
//
// `once` 单独一条：它是"就在那一刻跑一次"的语义（像闹钟），进程没在跑就是错过了——
// 补跑一个早已过去的 `once` 只会让人意外（"我约的是凌晨三点的迁移"），所以按计划书
// 原文置 inactive，并把原因如实写进定义文件（不静默消失）。daily/weekly 则相反：
// "每天早上 9 点的巡检"在当晚补上仍有价值，故补最近一次。
func (s Schedule) Startup(lastFiredAt *time.Time, now time.Time) (StartupAction, Occurrence, error) {
	if s.Kind == KindOnce {
		if lastFiredAt != nil {
			return StartupInactive, Occurrence{}, nil
		}
		if s.At != nil && !s.At.After(now) {
			return StartupExpire, Occurrence{}, nil
		}
	}
	due, ok, err := s.NextDue(lastFiredAt, now)
	if err != nil {
		return "", Occurrence{}, err
	}
	if !ok {
		return StartupInactive, Occurrence{}, nil
	}
	if !due.At.After(now) {
		return StartupRecover, due, nil
	}
	return StartupWait, due, nil
}

// onceAt 处理 once 变体：按 pred 决定取不取。
func (s Schedule) onceAt(loc *time.Location, pred func(time.Time) bool) (Occurrence, bool, error) {
	if s.At == nil {
		return Occurrence{}, false, nil
	}
	at := s.At.In(loc)
	if !pred(at) {
		return Occurrence{}, false, nil
	}
	return Occurrence{At: at, Wall: at.Format(WallFormat), Timezone: s.Timezone}, true, nil
}

// firstMatching 从 t 起向后逐天找第一个满足 pred 的候选（比全窗口更省，且语义直白）。
func (s Schedule) firstMatching(t time.Time, loc *time.Location, pred func(time.Time) bool) (Occurrence, bool, error) {
	base := startOfLocalDay(t, loc)
	for i := 0; i <= scanDays; i++ {
		for _, c := range s.dayCandidates(base.AddDate(0, 0, i), loc) {
			if pred(c.At) {
				return c, true, nil
			}
		}
	}
	return Occurrence{}, false, nil
}

// dayCandidates 某本地日期的候选时刻（daily 恒一个；weekly 命中 daysOfWeek 才有一个）。
//
// DST 的两个方向都在这里落地：
//   - **空档**（春季前移：本地 02:30 不存在）：`time.Date` 把它规范化为之后的有效时刻
//     （02:30 → 03:30），语义等价于 openwork 的前移分支；这与墙钟不符时产 warning，
//     不静默。Go 标准库因此省掉了它那段 18 小时搜索窗口。
//   - **回拨**（秋季重复：本地 01:30 出现两次）：`time.Date` 返回**第一次**（夏令时那一次）。
//     行为写进注释与测试，不留给读代码的人猜。
func (s Schedule) dayCandidates(day time.Time, loc *time.Location) []Occurrence {
	if s.Kind != KindDaily && s.Kind != KindWeekly {
		return nil
	}
	if s.Hour == nil || s.Minute == nil {
		return nil
	}
	if s.Kind == KindWeekly && !containsWeekday(s.DaysOfWeek, int(day.Weekday())) {
		return nil
	}
	at := time.Date(day.Year(), day.Month(), day.Day(), *s.Hour, *s.Minute, 0, 0, loc)
	var warning string
	if got := at.In(loc); got.Hour() != *s.Hour || got.Minute() != *s.Minute {
		// 本地时刻不存在（春季前移的空档）。Go 的 `time.Date` 对**不存在的本地时刻**没有保证：
		// 它按切换后的偏移解释墙钟，得到的绝对时刻再换算回本地会落到空档**之前**
		// （实测 America/New_York 2026-03-08 02:30 → 06:30Z = 01:30 EST，比意图早了一小时）。
		// 所以这里自己前移：向后找第一个"本地时间真正越过请求墙钟"的时刻——即切换瞬间
		// （02:30 的请求 → 03:00 EDT），语义等价于 openwork 前移分支的"下一个有效分钟"。
		// 不能按"墙钟相等"去找：那样会一路找到**第二天**的 02:30（等于漏掉一整天）。
		if shifted, ok := shiftPastWall(at, loc, day, *s.Hour*60+*s.Minute); ok {
			g := shifted.In(loc)
			warning = "本地时刻 " + pad2(*s.Hour) + ":" + pad2(*s.Minute) + " 在 " + s.Timezone +
				" 的夏令时空档不存在，已前移到 " + pad2(g.Hour()) + ":" + pad2(g.Minute())
			at = shifted
		} else {
			warning = "本地时刻 " + pad2(*s.Hour) + ":" + pad2(*s.Minute) + " 在 " + s.Timezone +
				" 的夏令时空档不存在，且三小时内找不到前移目标（时区数据异常）"
		}
	}
	occ := Occurrence{At: at, Wall: at.Format(WallFormat), Timezone: s.Timezone}
	if warning != "" {
		occ.Warnings = append(occ.Warnings, warning)
	}
	return []Occurrence{occ}
}

// shiftPastWall 从 cand（空档内的 naive 结果）起逐分钟向后，找第一个本地日期仍为 day、
// 且本地墙钟已越过 wantMinute 的时刻——对 DST 前移就是切换瞬间（02:30 的请求 → 03:00）。
// 扫描上限三小时：现实中的 DST 空档 ≤ 2h（含整点前移与 30 分钟制），够用且必然终止。
func shiftPastWall(cand time.Time, loc *time.Location, day time.Time, wantMinute int) (time.Time, bool) {
	for i := 1; i <= 3*60; i++ {
		c := cand.Add(time.Duration(i) * time.Minute)
		l := c.In(loc)
		if l.Year() != day.Year() || l.Month() != day.Month() || l.Day() != day.Day() {
			continue
		}
		if l.Hour()*60+l.Minute() >= wantMinute {
			return c, true
		}
	}
	return cand, false
}

func startOfLocalDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	// 用正午做日期锚点：DST 切换日的 00:00 可能不存在（前移到 01:00），正午永远存在。
	return time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, loc)
}

func containsWeekday(days []int, wd int) bool {
	for _, d := range days {
		if d == wd {
			return true
		}
	}
	return false
}

func pad2(n int) string { return fmt.Sprintf("%02d", n) }
