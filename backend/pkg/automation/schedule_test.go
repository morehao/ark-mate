package automation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
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
			if got := aclCode(err); got != c.wantErr {
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
	for _, k := range []string{"last_fired_at", "last_conversation_id", "last_status", "last_reason", "consecutive_failures", "recent_runs"} {
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
func TestAutomationValidate(t *testing.T) {
	base := func() *Automation {
		return &Automation{
			ID: "a1", Name: "每日巡检", Enabled: true,
			Schedule: daily("Asia/Shanghai", 9, 0),
			Target:   Target{AgentID: "ag1", Cwd: "/tmp/ws", Prompt: "跑一遍依赖巡检"},
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
		{"缺 agent_id", func(a *Automation) { a.Target.AgentID = "" }},
		{"缺 prompt", func(a *Automation) { a.Target.Prompt = "" }},
		{"缺 cwd", func(a *Automation) { a.Target.Cwd = "" }},
		{"cwd 非绝对路径", func(a *Automation) { a.Target.Cwd = "ws/相对" }},
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

func ptrTime(t time.Time) *time.Time { return &t }

// aclCode 取出带码错误的码（本包的校验错误一律用 pkg/acl 的可操作错误）。
func aclCode(err error) string {
	var ae *acl.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}
