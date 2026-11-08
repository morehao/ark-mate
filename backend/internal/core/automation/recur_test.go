package automation

import (
	"strings"
	"testing"
	"time"
)

// recur_test.go —— rrule 解析器的边界表驱动单测（照抄 dsh rrule.ts 的拒绝清单）。
//
// 期望值来源：手算 / RFC 5545 语义 / dsh `packages/util/time/src/rrule.ts` 的边界规则，
// 不是从被测实现里抄出来的自洽数字。既有 5 种的用例全在 schedule_test.go，本文件只测 rrule。

func rruleSched(expr string) Schedule {
	return Schedule{Kind: KindRRule, RRule: expr}
}

// TestRRuleValidate rrule 语法/边界校验：合法放行、非法一律 `invalid_schedule`。
func TestRRuleValidate(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"daily 正常", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY", false},
		{"monthly 2WE 正常", "DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYDAY=2WE", false},
		{"COUNT=1 正常", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=1", false},
		{"UNTIL 正常", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;UNTIL=20260105T090000Z", false},
		{"TZID 本地 正常", "DTSTART;TZID=Asia/Shanghai:20260101T090000\nRRULE:FREQ=WEEKLY;BYDAY=MO,FR", false},
		{"带 CRLF 与空行 正常", "\r\nDTSTART:20260101T090000Z\r\nRRULE:FREQ=DAILY\r\n", false},

		{"空表达式", "", true},
		{"缺 RRULE 行", "DTSTART:20260101T090000Z", true},
		{"多一行", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY\nX:1", true},
		{"行顺序反", "RRULE:FREQ=DAILY\nDTSTART:20260101T090000Z", true},
		{"SECONDLY 拒绝", "DTSTART:20260101T090000Z\nRRULE:FREQ=SECONDLY", true},
		{"FREQ 非法", "DTSTART:20260101T090000Z\nRRULE:FREQ=FORTNIGHTLY", true},
		{"缺 FREQ", "DTSTART:20260101T090000Z\nRRULE:COUNT=2", true},
		{"COUNT+UNTIL 互斥", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=2;UNTIL=20260105T090000Z", true},
		{"COUNT=0 拒绝", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=0", true},
		{"COUNT 超 1000", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=1001", true},
		{"字段名重复", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;FREQ=WEEKLY", true},
		{"不支持的字段", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;BYFOO=1", true},
		{"不存在 DTSTART（DST 空档）", "DTSTART;TZID=America/New_York:20260308T023000\nRRULE:FREQ=DAILY", true},
		{"DTSTART 非 Z 的 UTC 形态", "DTSTART:20260101T090000\nRRULE:FREQ=DAILY", true},
		{"DTSTART TZID 带 Z", "DTSTART;TZID=Asia/Shanghai:20260101T090000Z\nRRULE:FREQ=DAILY", true},
		{"未知 TZID", "DTSTART;TZID=Mars/Olympus:20260101T090000\nRRULE:FREQ=DAILY", true},
		{"DTSTART 非真实日期", "DTSTART:20261301T090000Z\nRRULE:FREQ=DAILY", true},
		{"BYSECOND 多值", "DTSTART:20260101T090000Z\nRRULE:FREQ=MINUTELY;BYSECOND=0,30", true},
		{"MINUTELY 带 BY 部分", "DTSTART:20260101T090000Z\nRRULE:FREQ=MINUTELY;BYHOUR=1", true},
		{"BYMONTHDAY 非 MONTHLY/YEARLY", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;BYMONTHDAY=1", true},
		{"序数 BYDAY 非 MONTHLY/YEARLY", "DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;BYDAY=2WE", true},
		{"序数 BYDAY + BYWEEKNO", "DTSTART:20260101T090000Z\nRRULE:FREQ=YEARLY;BYDAY=2WE;BYWEEKNO=1", true},
		{"BYSETPOS 无其它 BY", "DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYSETPOS=1", true},
		{"BYWEEKNO 非 YEARLY", "DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYWEEKNO=1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := rruleSched(c.expr).Validate()
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望拒绝，got nil（expr=%q）", c.expr)
				}
				if code := acpCode(err); code != "invalid_schedule" {
					t.Fatalf("错误码应为 invalid_schedule，got %q（err=%v）", code, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，got %v", err)
			}
		})
	}
}

// TestRRuleDensityReject 一个 FREQ 周期候选 >10080 拒绝（density）。
func TestRRuleDensityReject(t *testing.T) {
	// FREQ=MINUTELY;INTERVAL=1;BYHOUR=全 24 小时 -> 每周期 24 个候选，不超；改用 BYHOUR+BYMINUTE 组合造大数。
	// 用一个真实可超限的形态：FREQ=DAILY 的 periodDays=1，靠 BYHOUR(24)*BYMINUTE(60)=1440 不超；
	// 但 FREQ=MINUTELY 带 BYHOUR 不允许。改用 FREQ=YEARLY BYMONTH=全12 * BYMONTHDAY 多值 * 时间。
	// 直接构造：FREQ=YEARLY;BYMONTH=1..12;BYMONTHDAY=1..28;BYHOUR=0..23 (periodDays=12*28=336, times=24 -> 8064, 不超)
	// 再乘 BYMINUTE 全 60 -> 336*24*60=483840 超限。
	var expr strings.Builder
	expr.WriteString("DTSTART:20260101T000000Z\nRRULE:FREQ=YEARLY;BYMONTH=1,2,3,4,5,6,7,8,9,10,11,12")
	expr.WriteString(";BYMONTHDAY=1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28")
	expr.WriteString(";BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23")
	expr.WriteString(";BYMINUTE=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59")
	err := rruleSched(expr.String()).Validate()
	if err == nil {
		t.Fatal("候选超上限应被拒绝（density）")
	}
	if !strings.Contains(err.Error(), "上限") && !strings.Contains(err.Error(), "发生") {
		t.Fatalf("应是候选超限错误，got %v", err)
	}
}

// TestRRuleNextAfter 各类规则的下一次时刻（严格晚于）。
func TestRRuleNextAfter(t *testing.T) {
	cases := []struct {
		name string
		expr string
		from time.Time
		want time.Time
		ok   bool
	}{
		{
			"每月第 2 个周三：2026-01 的 2WE 是 01-14",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYDAY=2WE",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 14, 9, 0, 0, 0, time.UTC), true,
		},
		{
			"每月最后一天（BYMONTHDAY=-1）1 月是 01-31",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYMONTHDAY=-1",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC), true,
		},
		{
			"COUNT=1 严格晚于起点",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=1",
			time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC), true,
		},
		{
			"COUNT=1 之后无下一次",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;COUNT=1",
			time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			time.Time{}, false,
		},
		{
			"UNTIL 到期后无下一次",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;UNTIL=20260103T090000Z",
			time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC),
			time.Time{}, false,
		},
		{
			"UNTIL 边界内：01-03 09:00 前取 01-03",
			"DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY;UNTIL=20260103T090000Z",
			time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC), true,
		},
		{
			"工作日 9 点（周一到周五）从周五晚到下周一",
			"DTSTART:20260105T090000Z\nRRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0",
			// 2026-01-09 是周五；其 10:00 之后 → 下周一 01-12 09:00
			time.Date(2026, 1, 9, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC), true,
		},
		{
			"TZID 时区：Asia/Shanghai 每天 09:00 落 01:00Z",
			"DTSTART;TZID=Asia/Shanghai:20260101T090000\nRRULE:FREQ=DAILY",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			occ, ok, err := rruleSched(c.expr).NextAfter(c.from)
			if err != nil {
				t.Fatalf("NextAfter: %v", err)
			}
			if ok != c.ok {
				t.Fatalf("ok=%v，期望 %v", ok, c.ok)
			}
			if c.ok && !occ.At.Equal(c.want) {
				t.Fatalf("NextAfter = %s，期望 %s", occ.At.UTC(), c.want)
			}
			if c.ok && (occ.Wall == "" || occ.Timezone == "") {
				t.Fatalf("Occurrence 应带墙钟与时区: %+v", occ)
			}
		})
	}
}

// TestRRuleDSTGapSkipped 后续不存在的本地发生时点跳过（且不消耗 COUNT）。
// America/New_York 2026-03-08 02:30 落入春季空档。
func TestRRuleDSTGapSkipped(t *testing.T) {
	s := rruleSched("DTSTART;TZID=America/New_York:20260307T023000\nRRULE:FREQ=DAILY")
	// 03-07 02:30 EST = 07:30Z
	occ, ok, err := s.NextAfter(time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	if !occ.At.Equal(time.Date(2026, 3, 7, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("03-07 应 = 07:30Z，got %s", occ.At.UTC())
	}
	// 下一次跳过 03-08（空档），落到 03-09 02:30 EDT = 06:30Z
	next, ok, err := s.NextAfter(occ.At)
	if err != nil || !ok {
		t.Fatalf("NextAfter(03-07): %v ok=%v", err, ok)
	}
	if !next.At.Equal(time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("应跳过 03-08 空档落到 03-09 06:30Z，got %s", next.At.UTC())
	}
}

// TestRRuleCountSkipsGapWithoutConsuming COUNT=3 跨空档：有效发生时点是 03-07 / 03-09 / 03-10，
// 03-08 被跳过**不消耗** COUNT。
func TestRRuleCountSkipsGapWithoutConsuming(t *testing.T) {
	s := rruleSched("DTSTART;TZID=America/New_York:20260307T023000\nRRULE:FREQ=DAILY;COUNT=3")
	var got []time.Time
	cursor := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		occ, ok, err := s.NextAfter(cursor)
		if err != nil {
			t.Fatalf("NextAfter: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, occ.At.UTC())
		cursor = occ.At
	}
	want := []time.Time{
		time.Date(2026, 3, 7, 7, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 10, 6, 30, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("COUNT=3 应恰好 3 次（空档不消耗），got %d：%v", len(got), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("第 %d 次 = %s，期望 %s", i+1, got[i], want[i])
		}
	}
}

// TestRRuleDSTFallBackEarlier 重叠（秋季回拨）取**较早**的时刻。
// America/New_York 2026-11-01 01:30 出现两次，应取 EDT（-0400）= 05:30Z。
func TestRRuleDSTFallBackEarlier(t *testing.T) {
	s := rruleSched("DTSTART;TZID=America/New_York:20261101T013000\nRRULE:FREQ=DAILY;COUNT=1")
	occ, ok, err := s.NextAfter(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("NextAfter: %v ok=%v", err, ok)
	}
	if !occ.At.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("应取较早的 EDT 01:30（05:30Z），got %s", occ.At.UTC())
	}
	loc := mustLoc(t, "America/New_York")
	if off := occ.At.In(loc).Format("-0700"); off != "-0400" {
		t.Fatalf("应是回拨前的 EDT（-0400），got %s", off)
	}
}

// TestRRuleLatestAtOrBefore 停机只补最近一次：取不晚于 now 的最近一个发生时点。
func TestRRuleLatestAtOrBefore(t *testing.T) {
	s := rruleSched("DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY")
	got, ok, err := s.LatestAtOrBefore(time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("LatestAtOrBefore: %v ok=%v", err, ok)
	}
	if !got.At.Equal(time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("应取 01-04 09:00，got %s", got.At.UTC())
	}
	// 恰好等于到期时刻 → 含该时刻本身
	if g, _, _ := s.LatestAtOrBefore(time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)); !g.At.Equal(time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("边界应包含该时刻本身，got %s", g.At.UTC())
	}
	// now 早于 DTSTART → 无
	if _, ok, _ := s.LatestAtOrBefore(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)); ok {
		t.Fatal("now 早于 DTSTART 时不该有最近一次")
	}
}

// TestRRuleStartupRecovery Startup 对 rrule 走"非 once"既有分支（只补最近一次）：
// rrule 停机错过 → recovery；未到期 → wait。确认 rrule 复用既有 NextDue→LatestAtOrBefore 口径。
func TestRRuleStartupRecovery(t *testing.T) {
	s := rruleSched("DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY")
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	last := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	action, occ, err := s.Startup(&last, now)
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if action != StartupRecover {
		t.Fatalf("rrule 停机错过应 recovery，got %s", action)
	}
	if !occ.At.Equal(time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("补最近一次应是 01-04 09:00，got %s", occ.At.UTC())
	}
	// 未到期（lastFired=01-03 09:00，now=01-04 08:00 → 下一次 01-04 09:00 在未来）→ wait
	last3 := time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	early := time.Date(2026, 1, 4, 8, 0, 0, 0, time.UTC)
	action2, occ2, err := s.Startup(&last3, early)
	if err != nil {
		t.Fatalf("Startup(early): %v", err)
	}
	if action2 != StartupWait || !occ2.At.Equal(time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("未到期应 wait 到 01-04 09:00，got %s / %s", action2, occ2.At.UTC())
	}
}

// TestRRulePreview 预览连续多次与手算一致（复用既有 Preview 路径）。
func TestRRulePreview(t *testing.T) {
	got, err := Preview(rruleSched("DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 3)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := []time.Time{
		time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 条，got %d", len(want), len(got))
	}
	for i := range want {
		if !got[i].At.Equal(want[i]) {
			t.Fatalf("第 %d 条 = %s，期望 %s", i+1, got[i].At.UTC(), want[i])
		}
	}
}

// TestRRuleAuthoritativeNoNormalize 存权威表达式：DTSTART 的 TZID 与时区被保留，Occurrence.Timezone
// 回带该时区（不规范化成别的形态）。
func TestRRuleAuthoritativeNoNormalize(t *testing.T) {
	s := rruleSched("DTSTART;TZID=Asia/Shanghai:20260101T090000\nRRULE:FREQ=DAILY")
	occ, _, err := s.NextAfter(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if occ.Timezone != "Asia/Shanghai" {
		t.Fatalf("Occurrence.Timezone 应保留 DTSTART 的 TZID（Asia/Shanghai），got %q", occ.Timezone)
	}
}
