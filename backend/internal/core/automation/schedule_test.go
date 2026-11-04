package automation

import (
	"errors"
	"github.com/morehao/ark-mate/internal/core/failure"
	"strings"
	"testing"
	"time"
)

// 期望值来源：`python3 -c "zoneinfo"` 与 `TZ=... date`（系统 tzdata）手算对照，
// 不是从被测实现里抄出来的自洽数字。

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("加载时区 %s: %v", name, err)
	}
	return loc
}

func at(t *testing.T, tz, wall string) time.Time {
	t.Helper()
	loc := mustLoc(t, tz)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if v, err := time.ParseInLocation(layout, wall, loc); err == nil {
			return v
		}
	}
	t.Fatalf("解析 %s %s 失败", tz, wall)
	return time.Time{}
}

func utc(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func daily(tz string, hour, minute int) Schedule {
	return Schedule{Kind: KindDaily, Timezone: tz, Hour: &hour, Minute: &minute}
}

func weekly(tz string, hour, minute int, days ...int) Schedule {
	return Schedule{Kind: KindWeekly, Timezone: tz, Hour: &hour, Minute: &minute, DaysOfWeek: days}
}

func TestScheduleValidate(t *testing.T) {
	h9, m0, bad := 9, 0, 24
	cases := []struct {
		name    string
		s       Schedule
		wantErr string // 期望的码；空 = 通过
	}{
		{"daily 正常", daily("Asia/Shanghai", 9, 0), ""},
		{"weekly 正常", weekly("UTC", 1, 2, 1, 3), ""},
		{"once 正常", Schedule{Kind: KindOnce, Timezone: "UTC", At: ptrTime(utc("2026-01-01T00:00:00Z"))}, ""},
		{"kind 非法", Schedule{Kind: "cron", Timezone: "UTC"}, "invalid_schedule"},
		{"时区缺失", Schedule{Kind: KindDaily, Hour: &h9, Minute: &m0}, "invalid_timezone"},
		{"时区未知", daily("Mars/Olympus", 9, 0), "invalid_timezone"},
		{"daily 缺 hour", Schedule{Kind: KindDaily, Timezone: "UTC", Minute: &m0}, "invalid_schedule"},
		{"hour 越界", daily("UTC", bad, 0), "invalid_schedule"},
		{"weekly 空 days", weekly("UTC", 9, 0), "invalid_schedule"},
		{"weekly day 越界", weekly("UTC", 9, 0, 7), "invalid_schedule"},
		{"once 缺 at", Schedule{Kind: KindOnce, Timezone: "UTC"}, "invalid_schedule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.s.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("期望报错，got nil")
			}
			if got := acpCode(err); got != c.wantErr {
				t.Fatalf("期望码 %s，got %s（%v）", c.wantErr, got, err)
			}
		})
	}
}

// TestNormalizeDaysOfWeek 规范化必须在算/存之前：`[3,1,3]` 与 `[1,3]` 语义完全相同。
func TestNormalizeDaysOfWeek(t *testing.T) {
	s := weekly("UTC", 9, 0, 3, 1, 3, 0)
	s.Normalize()
	if got := len(s.DaysOfWeek); got != 3 {
		t.Fatalf("期望去重后 3 个，got %d（%v）", got, s.DaysOfWeek)
	}
	for i, want := range []int{0, 1, 3} {
		if s.DaysOfWeek[i] != want {
			t.Fatalf("期望升序 %v，got %v", []int{0, 1, 3}, s.DaysOfWeek)
		}
	}
	// 非 weekly 不动
	d := daily("UTC", 9, 0)
	d.DaysOfWeek = []int{5, 5}
	d.Normalize()
	if len(d.DaysOfWeek) != 2 {
		t.Fatalf("daily 不该被动规范化: %v", d.DaysOfWeek)
	}
}

func TestNextAfterDaily(t *testing.T) {
	s := daily("Asia/Shanghai", 9, 0)
	cases := []struct {
		from time.Time
		want time.Time
		ok   bool
	}{
		// 同一天 09:00 之前 → 当天（CST 09:00 = 01:00Z）
		{at(t, "Asia/Shanghai", "2026-01-01 08:59"), utc("2026-01-01T01:00:00Z"), true},
		// 正好等于 09:00 → 严格晚于，取次日
		{utc("2026-01-01T01:00:00Z"), utc("2026-01-02T01:00:00Z"), true},
		// 09:00 之后 → 次日
		{at(t, "Asia/Shanghai", "2026-01-01 09:00:01"), utc("2026-01-02T01:00:00Z"), true},
	}
	for _, c := range cases {
		got, ok, err := s.NextAfter(c.from)
		if err != nil {
			t.Fatalf("NextAfter(%s): %v", c.from, err)
		}
		if ok != c.ok || !got.At.Equal(c.want) {
			t.Fatalf("NextAfter(%s) = %s(ok=%v)，期望 %s", c.from, got.At.UTC(), ok, c.want)
		}
		if got.Wall == "" || got.Timezone != "Asia/Shanghai" {
			t.Fatalf("Occurrence 应带墙钟与时区: %+v", got)
		}
	}
}

func TestNextAfterOnce(t *testing.T) {
	at0 := utc("2026-05-01T10:00:00Z")
	s := Schedule{Kind: KindOnce, Timezone: "UTC", At: &at0}
	if got, ok, _ := s.NextAfter(utc("2026-04-30T00:00:00Z")); !ok || !got.At.Equal(at0) {
		t.Fatalf("未到点应返回该时刻，got %v ok=%v", got.At, ok)
	}
	if _, ok, _ := s.NextAfter(at0); ok {
		t.Fatal("once 过后不该再有下一次")
	}
	if _, ok, _ := s.NextAfter(utc("2026-05-02T00:00:00Z")); ok {
		t.Fatal("once 过期后不该再有下一次")
	}
}

// TestWeeklyDays 0=周日（与 time.Weekday 对齐）；2026-01-05 是周一、01-07 是周三。
func TestWeeklyDays(t *testing.T) {
	s := weekly("Asia/Shanghai", 9, 0, 1, 3)
	got, ok, err := s.NextAfter(at(t, "Asia/Shanghai", "2026-01-05 10:00"))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	if !got.At.Equal(utc("2026-01-07T01:00:00Z")) {
		t.Fatalf("周一 10:00 之后应是周三 09:00（01:00Z），got %s", got.At.UTC())
	}
	// 周日 0：2026-01-04 是周日
	sun := weekly("Asia/Shanghai", 9, 0, 0)
	got, ok, _ = sun.NextAfter(at(t, "Asia/Shanghai", "2026-01-03 23:00"))
	if !ok || !got.At.Equal(utc("2026-01-04T01:00:00Z")) {
		t.Fatalf("周六 23:00 之后应是周日 09:00，got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestDSTForwardShift 春季前移：本地 02:30 不存在，`time.Date` 前移到 03:30 EDT（= 07:30Z），
// 且**必须产 warning**——前移是"我们替你改了时间"，不说是最坏的做法。
func TestDSTForwardShift(t *testing.T) {
	s := daily("America/New_York", 2, 30)
	got, ok, err := s.NextAfter(at(t, "America/New_York", "2026-03-08 00:00"))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	if !got.At.Equal(utc("2026-03-08T07:00:00Z")) {
		t.Fatalf("期望前移到空档后的第一个有效分钟 03:00 EDT（07:00Z），got %s", got.At.UTC())
	}
	if len(got.Warnings) == 0 {
		t.Fatal("DST 空档必须产 warning（否则用户永远不知道时间被改了）")
	}
	if !strings.Contains(got.Warnings[0], "空档") || !strings.Contains(got.Warnings[0], "03:00") {
		t.Fatalf("warning 应说清前移结果: %v", got.Warnings)
	}
	// 非 DST 日的候选不带 warning
	plain, _, _ := s.NextAfter(at(t, "America/New_York", "2026-03-09 00:00"))
	if len(plain.Warnings) != 0 {
		t.Fatalf("非切换日不该有 warning: %v", plain.Warnings)
	}
}

// TestDSTFallBackFirstOccurrence 秋季回拨：本地 01:30 出现两次，取**第一次**（EDT，-0400）。
// 这是刻意写进文档与测试的行为，不留给实现者猜。
func TestDSTFallBackFirstOccurrence(t *testing.T) {
	s := daily("America/New_York", 1, 30)
	got, _, err := s.NextAfter(at(t, "America/New_York", "2026-11-01 00:00"))
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if !got.At.Equal(utc("2026-11-01T05:30:00Z")) {
		t.Fatalf("应取第一次（EDT 01:30 = 05:30Z），got %s", got.At.UTC())
	}
	if off := got.At.In(mustLoc(t, "America/New_York")).Format("-0700"); off != "-0400" {
		t.Fatalf("应是回拨前的 EDT（-0400），got %s", off)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("墙钟与请求一致时不该有 warning: %v", got.Warnings)
	}
}

// TestLatestAtOrBefore 停机补跑的取数口径：只取最近一次，不重放积压。
func TestLatestAtOrBefore(t *testing.T) {
	s := daily("Asia/Shanghai", 9, 0)
	got, ok, err := s.LatestAtOrBefore(at(t, "Asia/Shanghai", "2026-01-04 12:00"))
	if err != nil || !ok {
		t.Fatalf("LatestAtOrBefore: %v ok=%v", err, ok)
	}
	if !got.At.Equal(utc("2026-01-04T01:00:00Z")) {
		t.Fatalf("期望 01-04 09:00（当天那次），got %s", got.At.UTC())
	}
	// 恰好等于到期时刻 → 算"不晚于"，取它本身
	if got, _, _ := s.LatestAtOrBefore(utc("2026-01-04T01:00:00Z")); !got.At.Equal(utc("2026-01-04T01:00:00Z")) {
		t.Fatalf("边界应包含该时刻本身，got %s", got.At.UTC())
	}
	// 当天 09:00 之前 → 前一天 09:00（窗口内的最近一次）
	if got, ok, _ := s.LatestAtOrBefore(at(t, "Asia/Shanghai", "2026-01-01 08:00")); !ok ||
		!got.At.Equal(utc("2025-12-31T01:00:00Z")) {
		t.Fatalf("应为前一天 09:00，got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestNextDue 调度循环唯一的入口：正常运行 + 停机补跑用同一条路径。
func TestNextDue(t *testing.T) {
	s := daily("Asia/Shanghai", 9, 0)
	noon := at(t, "Asia/Shanghai", "2026-01-04 12:00")
	morning := at(t, "Asia/Shanghai", "2026-01-04 08:00")
	cases := []struct {
		name      string
		lastFired *time.Time
		now       time.Time
		want      time.Time
		ok        bool
	}{
		{"从未跑过且今天已过点 → 明天（新建不补跑）", nil, noon, utc("2026-01-05T01:00:00Z"), true},
		{"从未跑过且今天没到点 → 今天", nil, morning, utc("2026-01-04T01:00:00Z"), true},
		{"昨天跑过 → 今天（到期）", ptrTime(utc("2026-01-03T01:00:00Z")), noon, utc("2026-01-04T01:00:00Z"), true},
		{"三天前跑过 → 今天（只补最近一次，不重放）", ptrTime(utc("2026-01-01T01:00:00Z")), noon, utc("2026-01-04T01:00:00Z"), true},
		{"刚跑过 → 明天", ptrTime(utc("2026-01-04T01:00:00Z")), noon, utc("2026-01-05T01:00:00Z"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok, err := s.NextDue(c.lastFired, c.now)
			if err != nil {
				t.Fatalf("NextDue: %v", err)
			}
			if ok != c.ok || !got.At.Equal(c.want) {
				t.Fatalf("NextDue(%v, %s) = %s(ok=%v)，期望 %s", c.lastFired, c.now, got.At.UTC(), ok, c.want)
			}
		})
	}
	// once 到期未跑：NextDue 返回那一次（调用方决定补还是置 inactive）
	at0 := utc("2026-05-01T10:00:00Z")
	once := Schedule{Kind: KindOnce, Timezone: "UTC", At: &at0}
	got, ok, _ := once.NextDue(nil, utc("2026-05-01T11:00:00Z"))
	if !ok || !got.At.Equal(at0) {
		t.Fatalf("once 过期未跑应返回该时刻，got %s ok=%v", got.At, ok)
	}
	// once 已跑：没有下一次
	if _, ok, _ := once.NextDue(&at0, utc("2026-05-01T11:00:00Z")); ok {
		t.Fatal("once 跑过后不该再有下一次")
	}
}

// TestStartup ADR-9：daily/weekly 补最近一次；once 过期则置 inactive（不补）。
func TestStartup(t *testing.T) {
	now := at(t, "Asia/Shanghai", "2026-01-04 12:00")
	cases := []struct {
		name      string
		s         Schedule
		lastFired *time.Time
		want      StartupAction
		wantAt    time.Time
	}{
		{"daily 停机错过 → recovery（最近一次）", daily("Asia/Shanghai", 9, 0),
			ptrTime(utc("2026-01-01T01:00:00Z")), StartupRecover, utc("2026-01-04T01:00:00Z")},
		{"daily 未到期 → wait", daily("Asia/Shanghai", 9, 0), nil, StartupWait, utc("2026-01-05T01:00:00Z")},
		{"weekly 停机错过 → recovery（最近一次是 12-29，不是更早的 12-22）", weekly("Asia/Shanghai", 9, 0, 1),
			ptrTime(utc("2025-12-22T01:00:00Z")), StartupRecover, utc("2025-12-29T01:00:00Z")},
		{"once 过期未跑 → expire（不补）", Schedule{Kind: KindOnce, Timezone: "UTC",
			At: ptrTime(utc("2026-01-01T00:00:00Z"))}, nil, StartupExpire, time.Time{}},
		{"once 已跑 → inactive", Schedule{Kind: KindOnce, Timezone: "UTC",
			At: ptrTime(utc("2026-01-01T00:00:00Z"))}, ptrTime(utc("2026-01-01T00:00:00Z")), StartupInactive, time.Time{}},
		{"once 未到点 → wait", Schedule{Kind: KindOnce, Timezone: "UTC",
			At: ptrTime(utc("2026-02-01T00:00:00Z"))}, nil, StartupWait, utc("2026-02-01T00:00:00Z")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, occ, err := c.s.Startup(c.lastFired, now)
			if err != nil {
				t.Fatalf("Startup: %v", err)
			}
			if got != c.want {
				t.Fatalf("动作 = %s，期望 %s", got, c.want)
			}
			if c.wantAt.IsZero() {
				if !occ.At.IsZero() {
					t.Fatalf("该动作不该带到期时刻: %v", occ.At)
				}
				return
			}
			if !occ.At.Equal(c.wantAt) {
				t.Fatalf("到期 = %s，期望 %s", occ.At.UTC(), c.wantAt)
			}
		})
	}
}

// TestPreview Gate 5：未来 5 次与独立手算一致；跨 DST 日期 warnings 非空。
func TestPreview(t *testing.T) {
	got, err := Preview(daily("Asia/Shanghai", 9, 0), at(t, "Asia/Shanghai", "2026-01-01 00:00"), 5)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := []time.Time{
		utc("2026-01-01T01:00:00Z"), utc("2026-01-02T01:00:00Z"), utc("2026-01-03T01:00:00Z"),
		utc("2026-01-04T01:00:00Z"), utc("2026-01-05T01:00:00Z"),
	}
	if len(got) != len(want) {
		t.Fatalf("期望 5 条，got %d", len(got))
	}
	for i := range want {
		if !got[i].At.Equal(want[i]) {
			t.Fatalf("第 %d 条 = %s，期望 %s", i+1, got[i].At.UTC(), want[i])
		}
	}
	// 跨 DST：America/New_York daily 02:30 从 03-06 起 5 次，03-08 那次被前移
	dst, err := Preview(daily("America/New_York", 2, 30), at(t, "America/New_York", "2026-03-06 00:00"), 5)
	if err != nil {
		t.Fatalf("Preview(DST): %v", err)
	}
	var warned int
	for _, o := range dst {
		if len(o.Warnings) > 0 {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("跨 DST 的 5 次里应恰好 1 次带 warning，got %d（%v）", warned, dst)
	}
	// once 只有一次，不足 count 不是错误
	once := Schedule{Kind: KindOnce, Timezone: "UTC", At: ptrTime(utc("2026-05-01T00:00:00Z"))}
	one, err := Preview(once, utc("2026-01-01T00:00:00Z"), 5)
	if err != nil || len(one) != 1 {
		t.Fatalf("once 预览应恰好 1 条，got %d err=%v", len(one), err)
	}
	// 默认与上限
	if many, err := Preview(daily("UTC", 9, 0), utc("2026-01-01T00:00:00Z"), 0); err != nil || len(many) != PreviewCountDefault {
		t.Fatalf("count=0 应取默认 %d，got %d err=%v", PreviewCountDefault, len(many), err)
	}
	if _, err := Preview(daily("UTC", 9, 0), utc("2026-01-01T00:00:00Z"), PreviewCountMax+1); err == nil {
		t.Fatal("超过上限应报错")
	}
	// 非法 schedule 直接报错（预览也要过校验，否则用户照着一个跑不起来的表达式建定义）
	if _, err := Preview(daily("Mars/Olympus", 9, 0), utc("2026-01-01T00:00:00Z"), 5); err == nil {
		t.Fatal("非法时区应报错")
	}
}

// TestDecideClaim 三态认领 + 判据优先级（duplicate 比 overlap 硬）。
func TestDecideClaim(t *testing.T) {
	sched := utc("2026-01-04T01:00:00Z")
	cases := []struct {
		name      string
		lastFired *time.Time
		activeRun bool
		want      ClaimStatus
	}{
		{"从未跑过且无在跑 → claimed", nil, false, ClaimClaimed},
		{"同刻已认领 → duplicate", ptrTime(sched), false, ClaimDuplicate},
		{"更晚的已认领 → duplicate", ptrTime(utc("2026-01-05T01:00:00Z")), false, ClaimDuplicate},
		{"更早的已认领 + 无在跑 → claimed", ptrTime(utc("2026-01-03T01:00:00Z")), false, ClaimClaimed},
		{"上一次还在跑 → overlap", ptrTime(utc("2026-01-03T01:00:00Z")), true, ClaimOverlap},
		{"同刻已认领且上一次还在跑 → duplicate 优先", ptrTime(sched), true, ClaimDuplicate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(sched, c.lastFired, c.activeRun)
			if got.Status != c.want {
				t.Fatalf("状态 = %s，期望 %s", got.Status, c.want)
			}
			if got.Reason == "" {
				t.Fatal("Reason 必须非空：它会进 automation.run 事件给用户看")
			}
		})
	}
}

func TestStateMachine(t *testing.T) {
	if NormalizeState("") != StateActive || NormalizeState("垃圾") != StateActive {
		t.Fatal("空值/未知值应归一为 active")
	}
	if !Schedulable("") || Schedulable(StateInactive) || Schedulable(StateNeedsAttention) || Schedulable(StateArchived) {
		t.Fatal("只有 active 参与调度")
	}
	if !Terminal(StateArchived) || Terminal(StateActive) {
		t.Fatal("archived 是终态")
	}
	cases := []struct {
		from, to string
		want     bool
	}{
		{StateActive, StateInactive, true},
		{StateActive, StateNeedsAttention, true},
		{StateInactive, StateActive, true},
		{StateNeedsAttention, StateActive, true},
		{StateNeedsAttention, StateInactive, true},
		{StateActive, StateArchived, true},
		{StateInactive, StateArchived, true},
		{StateNeedsAttention, StateArchived, true},
		{StateArchived, StateArchived, true},
		{StateArchived, StateActive, false},
		{StateArchived, StateInactive, false},
		{StateArchived, StateNeedsAttention, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Fatalf("CanTransition(%s→%s) = %v，期望 %v", c.from, c.to, got, c.want)
		}
	}
}

// TestReservedKeys 平台独占键：用户写面不得写（`last_fired_at` 就是认领判据，能写它就能重放）。
func TestReservedKeys(t *testing.T) {
	for _, k := range []string{"last_fired_at", "last_session_id", "last_status", "last_reason", "consecutive_failures", "recent_runs"} {
		if !IsReservedKey(k) {
			t.Errorf("%s 应是平台独占键", k)
		}
	}
	// 用户可写的那几个不能被误伤（含 state：停用/归档/从 needs_attention 恢复都是用户操作）
	for _, k := range []string{"id", "name", "enabled", "schedule", "target", "max_run_duration", "state"} {
		if IsReservedKey(k) {
			t.Errorf("%s 不是平台独占键（用户必须能写）", k)
		}
	}
}

// TestAutomationValidate 定义级校验：跑不了的定义在装载期就该被拒（而不是每 tick 报一次）。
//
// v35 起缺省 delivery=session，故基础定义显式用 new_session（cwd 语义与旧版逐字相同）。
func TestAutomationValidate(t *testing.T) {
	base := func() *Automation {
		return &Automation{
			ID: "a1", Name: "每日巡检", Enabled: true,
			Schedule: daily("Asia/Shanghai", 9, 0),
			Target:   Target{Cwd: "/tmp/ws", Prompt: "跑一遍依赖巡检", Delivery: DeliveryNewSession},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("正常定义不该报错: %v", err)
	}
	cases := []struct {
		name   string
		modify func(*Automation)
	}{
		{"缺 id", func(a *Automation) { a.ID = "" }},
		{"缺 prompt", func(a *Automation) { a.Target.Prompt = "" }},
		{"new_session 缺 cwd", func(a *Automation) { a.Target.Cwd = "" }},
		{"new_session cwd 非绝对路径", func(a *Automation) { a.Target.Cwd = "ws/相对" }},
		{"调度非法", func(a *Automation) { a.Schedule = daily("Mars/Olympus", 9, 0) }},
		{"duration 非法", func(a *Automation) { a.MaxRunDuration = "半小时" }},
		{"duration 负数", func(a *Automation) { a.MaxRunDuration = "-5m" }},
		{"duration 超上限", func(a *Automation) { a.MaxRunDuration = "90m" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := base()
			c.modify(a)
			if err := a.Validate(); err == nil {
				t.Fatal("期望报错，got nil")
			}
		})
	}
	// 合法上限
	a := base()
	a.MaxRunDuration = "60m"
	if err := a.Validate(); err != nil {
		t.Fatalf("60m 应合法: %v", err)
	}
	if d, err := a.RunDuration(); err != nil || d != 60*time.Minute {
		t.Fatalf("RunDuration: %v %v", d, err)
	}
	// 空 = 0 = 不额外限制
	a.MaxRunDuration = ""
	if d, err := a.RunDuration(); err != nil || d != 0 {
		t.Fatalf("空 duration 应为 0: %v %v", d, err)
	}
}

// TestTargetDeliveryValidate delivery 归一/校验矩阵（v35）。
func TestTargetDeliveryValidate(t *testing.T) {
	cases := []struct {
		name    string
		target  Target
		wantErr string // 空 = 通过
	}{
		{"空 delivery 缺 session_id → 归一 session 后报未设", Target{Prompt: "p"}, "delivery_session_unset"},
		{"session 缺 session_id", Target{Prompt: "p", Delivery: DeliverySession}, "delivery_session_unset"},
		{"session 有 session_id → 通过（cwd 不要求）", Target{Prompt: "p", Delivery: DeliverySession, SessionID: "sess_1"}, ""},
		{"空 delivery + session_id → 通过", Target{Prompt: "p", SessionID: "sess_1"}, ""},
		{"new_session 缺 cwd", Target{Prompt: "p", Delivery: DeliveryNewSession}, "invalid_param"},
		{"new_session cwd 相对", Target{Prompt: "p", Delivery: DeliveryNewSession, Cwd: "rel/x"}, "invalid_param"},
		{"new_session 绝对 cwd → 通过", Target{Prompt: "p", Delivery: DeliveryNewSession, Cwd: "/abs/x"}, ""},
		{"非法 delivery", Target{Prompt: "p", Delivery: "cron", SessionID: "x"}, "invalid_param"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.target.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("期望报错，got nil")
			}
			if got := acpCode(err); got != c.wantErr {
				t.Fatalf("期望码 %s，got %s（%v）", c.wantErr, got, err)
			}
		})
	}
	// NormalizeDelivery：空串 = session，其它原样
	if NormalizeDelivery("") != DeliverySession || NormalizeDelivery("new_session") != DeliveryNewSession {
		t.Fatal("NormalizeDelivery 归一错误")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// acpCode 取出带码错误的码（本包的校验错误一律用 internal/infra/protocol/acp 的可操作错误）。
func acpCode(err error) string {
	var ae *failure.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// —— P0（v35）：interval + 受限 monthly ——

func interval(tz string, everySeconds int) Schedule {
	return Schedule{Kind: KindInterval, Timezone: tz, EverySeconds: &everySeconds}
}

func monthly(tz string, hour, minute, day int) Schedule {
	return Schedule{Kind: KindMonthly, Timezone: tz, Hour: &hour, Minute: &minute, Day: &day}
}

// TestValidateIntervalMonthly 两种新 kind 的校验矩阵（错误码仍是 invalid_schedule）。
func TestValidateIntervalMonthly(t *testing.T) {
	e60, e59, d1, d0, d32, h9, m0 := 60, 59, 1, 0, 32, 9, 0
	cases := []struct {
		name    string
		s       Schedule
		wantErr string
	}{
		{"interval 正常", interval("Asia/Shanghai", 3600), ""},
		{"interval 每分钟", interval("UTC", 60), ""},
		{"interval 缺 every_seconds", Schedule{Kind: KindInterval, Timezone: "UTC"}, "invalid_schedule"},
		{"interval <60", Schedule{Kind: KindInterval, Timezone: "UTC", EverySeconds: &e59}, "invalid_schedule"},
		{"interval 缺时区", Schedule{Kind: KindInterval, EverySeconds: &e60}, "invalid_timezone"},
		{"monthly 正常", monthly("Asia/Shanghai", 9, 0, 1), ""},
		{"monthly 31 号", monthly("Asia/Shanghai", 9, 0, 31), ""},
		{"monthly 缺 day", Schedule{Kind: KindMonthly, Timezone: "UTC", Hour: &h9, Minute: &m0}, "invalid_schedule"},
		{"monthly day=0", Schedule{Kind: KindMonthly, Timezone: "UTC", Hour: &h9, Minute: &m0, Day: &d0}, "invalid_schedule"},
		{"monthly day=32", Schedule{Kind: KindMonthly, Timezone: "UTC", Hour: &h9, Minute: &m0, Day: &d32}, "invalid_schedule"},
		{"monthly 缺 hour/minute", Schedule{Kind: KindMonthly, Timezone: "UTC", Day: &d1}, "invalid_schedule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.s.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("期望报错，got nil")
			}
			if got := acpCode(err); got != c.wantErr {
				t.Fatalf("期望码 %s，got %s（%v）", c.wantErr, got, err)
			}
		})
	}
}

// TestIntervalNextAfter 首次 / 续跑 / 严格晚于。
func TestIntervalNextAfter(t *testing.T) {
	s := interval("Asia/Shanghai", 3600) // 每 1 小时
	from := utc("2026-01-04T00:00:00Z")
	got, ok, err := s.NextAfter(from)
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	if !got.At.Equal(from.Add(time.Hour)) {
		t.Fatalf("interval 下次应为 from+1h，got %s", got.At.UTC())
	}
	if got.Timezone != "Asia/Shanghai" || got.Wall == "" {
		t.Fatalf("Occurrence 应带墙钟与时区: %+v", got)
	}
}

// TestIntervalNextDueFirstFire 从未跑过：首次 = now+every（在未来，wait，不立刻补跑）。
func TestIntervalNextDueFirstFire(t *testing.T) {
	s := interval("UTC", 3600)
	now := utc("2026-01-04T00:00:00Z")
	got, ok, err := s.NextDue(nil, now)
	if err != nil || !ok {
		t.Fatalf("NextDue: %v ok=%v", err, ok)
	}
	if !got.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("首次应 now+1h，got %s", got.At.UTC())
	}
	if act, _, _ := s.Startup(nil, now); act != StartupWait {
		t.Fatalf("首次应 wait，got %s", act)
	}
}

// TestIntervalRecovery 停机很久：只补最近一次（≤now 的最大 occurrence），不连续触发。
func TestIntervalRecovery(t *testing.T) {
	s := interval("UTC", 3600)
	now := utc("2026-01-04T12:00:00Z")
	// 上次在 2.5 小时前；错过 2 次（+1h 与 +2h 都在过去），最近一次 = lastFired+2h = now-0.5h
	last := now.Add(-150 * time.Minute)
	got, ok, err := s.NextDue(&last, now)
	if err != nil || !ok {
		t.Fatalf("NextDue: %v ok=%v", err, ok)
	}
	want := last.Add(2 * time.Hour) // = now-30m
	if !got.At.Equal(want) {
		t.Fatalf("应补最近一次 %s（now-30m），got %s", want, got.At.UTC())
	}
	if !got.At.Before(now) {
		t.Fatal("恢复点应在 now 之前（recovery）")
	}
	act, occ, _ := s.Startup(&last, now)
	if act != StartupRecover || !occ.At.Equal(want) {
		t.Fatalf("Startup 应 recovery 于 %s，got %s @ %s", want, act, occ.At.UTC())
	}
}

// TestIntervalNextDueFuture 刚跑过、下次仍在未来 → wait，返回那个未来点。
func TestIntervalNextDueFuture(t *testing.T) {
	s := interval("UTC", 3600)
	now := utc("2026-01-04T00:30:00Z")
	last := utc("2026-01-04T00:00:00Z")
	got, ok, _ := s.NextDue(&last, now)
	if !ok || !got.At.Equal(utc("2026-01-04T01:00:00Z")) {
		t.Fatalf("应返回 lastFired+1h=01:00Z，got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestIntervalDSTIrrelevant interval 加的是绝对时长：跨春季前移仍是整 1 小时。
func TestIntervalDSTIrrelevant(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	s := interval("America/New_York", 3600)
	// 2026-03-08 01:30 EST = 06:30Z；+1h = 07:30Z（= 03:30 EDT，墙钟跳过了 02:30）
	from := time.Date(2026, 3, 8, 1, 30, 0, 0, loc)
	got, _, err := s.NextAfter(from)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if !got.At.Equal(from.Add(time.Hour)) {
		t.Fatalf("绝对间隔应恒为 1h，got %s", got.At.UTC())
	}
	if w := got.Wall; !strings.Contains(w, "03:30") {
		t.Fatalf("墙钟应是 03:30（DST 前移后），got %s", w)
	}
}

// TestMonthlyNextAfter day=31 在缺此日的月份跳过：4 月无 31 → 5/31。
func TestMonthlyNextAfter(t *testing.T) {
	s := monthly("Asia/Shanghai", 9, 0, 31)
	got, ok, err := s.NextAfter(utc("2026-04-01T00:00:00Z"))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	// 2026-05-31 09:00 CST = 01:00Z
	if !got.At.Equal(utc("2026-05-31T01:00:00Z")) {
		t.Fatalf("应从 4 月跳到 5/31 09:00（01:00Z），got %s", got.At.UTC())
	}
}

// TestMonthlySkipsShortMonth 从 1/31 之后：2 月无 31 → 3/31（跨约 59 天，验证扫描窗口）。
func TestMonthlySkipsShortMonth(t *testing.T) {
	s := monthly("Asia/Shanghai", 9, 0, 31)
	from := utc("2026-01-31T01:00:01Z") // 1/31 09:00 CST 之后 1 秒
	got, ok, _ := s.NextAfter(from)
	if !ok || !got.At.Equal(utc("2026-03-31T01:00:00Z")) {
		t.Fatalf("应跳到 3/31 09:00（01:00Z），got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestMonthlyDay1Regular day=1 常规：月中之后到次月 1 号。
func TestMonthlyDay1Regular(t *testing.T) {
	s := monthly("Asia/Shanghai", 9, 0, 1)
	got, ok, _ := s.NextAfter(utc("2026-01-15T00:00:00Z"))
	if !ok || !got.At.Equal(utc("2026-02-01T01:00:00Z")) {
		t.Fatalf("应为 2/1 09:00（01:00Z），got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestMonthlyLatestAtOrBefore 停机补最近的 31 号：3/01 往前最近是 1/31（2 月无 31）。
func TestMonthlyLatestAtOrBefore(t *testing.T) {
	s := monthly("Asia/Shanghai", 9, 0, 31)
	got, ok, _ := s.LatestAtOrBefore(utc("2026-03-01T00:00:00Z"))
	if !ok || !got.At.Equal(utc("2026-01-31T01:00:00Z")) {
		t.Fatalf("应为 1/31 09:00（01:00Z），got %s ok=%v", got.At.UTC(), ok)
	}
}

// TestMonthlyStartupRecovery monthly day=1 停机错过 → 补最近一次。
func TestMonthlyStartupRecovery(t *testing.T) {
	s := monthly("Asia/Shanghai", 9, 0, 1)
	last := utc("2026-01-01T01:00:00Z")
	now := at(t, "Asia/Shanghai", "2026-03-15 12:00")
	act, occ, err := s.Startup(&last, now)
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if act != StartupRecover || !occ.At.Equal(utc("2026-03-01T01:00:00Z")) {
		t.Fatalf("应 recovery 于 3/1 09:00，got %s @ %s", act, occ.At.UTC())
	}
}

// TestMonthlyDSTShift monthly 复用同一套墙钟/DST 逻辑：day=8 02:30 在春季前移空档 → 前移 + warning。
func TestMonthlyDSTShift(t *testing.T) {
	s := monthly("America/New_York", 2, 30, 8)
	got, ok, err := s.NextAfter(at(t, "America/New_York", "2026-03-01 00:00"))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	// 2026-03-08 02:30 不存在 → 前移到 03:00 EDT = 07:00Z
	if !got.At.Equal(utc("2026-03-08T07:00:00Z")) {
		t.Fatalf("应前移到 03:00 EDT（07:00Z），got %s", got.At.UTC())
	}
	if len(got.Warnings) == 0 {
		t.Fatal("DST 空档必须产 warning")
	}
}

// TestPreviewIntervalMonthly Preview 复用统一 occurrence 接口，两种新 kind 都给出未来多次。
func TestPreviewIntervalMonthly(t *testing.T) {
	iv := interval("UTC", 3600)
	got, err := Preview(iv, utc("2026-01-01T00:00:00Z"), 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("interval 预览: %d err=%v", len(got), err)
	}
	for i := 1; i < len(got); i++ {
		if d := got[i].At.Sub(got[i-1].At); d != time.Hour {
			t.Fatalf("interval 预览间隔应恒 1h，第 %d 条 %v", i, d)
		}
	}
	mo := monthly("Asia/Shanghai", 9, 0, 1)
	ms, err := Preview(mo, utc("2026-01-01T00:00:00Z"), 3)
	if err != nil || len(ms) != 3 {
		t.Fatalf("monthly 预览: %d err=%v", len(ms), err)
	}
	for i, want := range []time.Time{
		utc("2026-01-01T01:00:00Z"), utc("2026-02-01T01:00:00Z"), utc("2026-03-01T01:00:00Z"),
	} {
		if !ms[i].At.Equal(want) {
			t.Fatalf("monthly 预览第 %d 条 = %s，期望 %s", i+1, ms[i].At.UTC(), want)
		}
	}
}
