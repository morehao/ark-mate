package automation

// recur.go —— 受限 RRULE 解析与发生时点计算（纯函数、零 IO、零时钟，与 schedule.go 一致）。
//
// 边界规则照抄 dsh `packages/util/time/src/rrule.ts`（其 schedule 子系统的拒绝清单）：
//
//   - 输入恰好两行：一行 DTSTART，一行 RRULE；DTSTART 走 UTC `Z` 或显式 `TZID=IANA`（拒绝浮动本地
//     时间/数字偏移/仅日期/额外行）。时区由 DTSTART 自带，不读 schedule.timezone。
//   - 只接受 FREQ / INTERVAL / COUNT / UNTIL / WKST / BYSECOND / BYMINUTE / BYHOUR / BYDAY /
//     BYMONTHDAY / BYYEARDAY / BYWEEKNO / BYMONTH / BYSETPOS；字段名与列表值唯一。
//   - 拒绝 SECONDLY（发生间隔必须 ≥60s）、多值 BYSECOND；COUNT 1–1000 且不与 UNTIL 共存；
//     一个 FREQ 周期候选 >10080 拒绝（density）。
//   - 各类组合约束（MINUTELY/HOURLY 不收 BY 部分、BYMONTHDAY 仅 MONTHLY/YEARLY、序数 BYDAY 仅
//     MONTHLY/YEARLY 且不能与 BYWEEKNO、BYSETPOS 需搭配其它 BY、BYWEEKNO/BYYEARDAY 仅 YEARLY）。
//   - 不存在的本地 DTSTART 拒绝；后续不存在的本地发生时点跳过（不消耗 COUNT）；重叠取较早时刻。
//   - 年份 0100–9999。
//
// **实现选型**：不引第三方 Go RRULE 库，而是自实现这一有界子集。理由：(1) 依赖面零新增，符合 ADR-3
// "无依赖"的取舍；(2) 边界规则与 dsh 完全一致、可逐条对照，避免不同库对 DST/不存在的墙钟处理差异；
// (3) 全部是纯函数，表驱动单测能钉死每个边界。计算用"本地墙钟朴素日期"枚举 SELECT 出候选，再逐条
// 用 time.Date 落到绝对时刻并做"墙钟回读"校验（dstResolve），与现有 dayCandidates 的 DST 认知一致。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
)

// 上限常量（照抄 dsh）。
const (
	maxRRuleCount          = 1000
	maxPeriodCandidates    = 10080 // 一周的每一分钟
	maxSetPositions        = 31
	minRRuleYear           = 100
	maxRRuleYear           = 9999
	maxRRuleSearchOccurs   = 100000 // 单次扫描枚举的硬上限（防病态规则无限扫）
)

var rruleWeekdays = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"} // 0=周一

var rruleSupportedParts = []string{
	"FREQ", "INTERVAL", "COUNT", "UNTIL", "WKST", "BYSECOND", "BYMINUTE", "BYHOUR", "BYDAY", "BYMONTHDAY",
	"BYYEARDAY", "BYWEEKNO", "BYMONTH", "BYSETPOS",
}

var rruleFrequencies = []string{"MINUTELY", "HOURLY", "DAILY", "WEEKLY", "MONTHLY", "YEARLY"}

type rruleListRange struct{ min, max int }

var rruleListRanges = map[string]rruleListRange{
	"BYSECOND":   {0, 59},
	"BYMINUTE":   {0, 59},
	"BYHOUR":     {0, 23},
	"BYMONTHDAY": {-31, 31},
	"BYYEARDAY":  {-366, 366},
	"BYWEEKNO":   {-53, 53},
	"BYMONTH":    {1, 12},
	"BYSETPOS":   {-366, 366},
}

// rruleSpec 解析并校验后的规则（枚举用）。字段语义见 rrule.ts 的 PreparedRRule。
type rruleSpec struct {
	norm      string // 两行原文（trim），存权威表达式用
	loc       *time.Location
	start     time.Time // DTSTART 的绝对时刻（用于锚定）
	startWall time.Time // DTSTART 的"墙钟朴素时间"（In(loc) 后取年月日时分秒，Location 用 UTC 承载）
	startUTC  bool      // DTSTART 是否是 Z（UTC）形态

	freq     string
	interval int
	count    int // 0 表示无 COUNT
	until    time.Time
	hasUntil bool
	wkst     int // 0=周一

	bySecond   []int
	byMinute   []int
	byHour     []int
	byMonthDay []int
	byYearDay  []int
	byWeekNo   []int
	byMonth    []int
	bySetPos   []int
	byDay      []byDayEntry

	// 内部枚举窗口（本地墙钟朴素时间的最远界）
	limitWall time.Time
}

// byDayEntry 一个 BYDAY 项（序数 + 星期，序数 0 表示无序号）。
type byDayEntry struct {
	ordinal int // 0=无；±1..±53
	weekday int // 0=MO..6=SU（与 rruleWeekdays 对齐）
}

// validateRRule 语法 + 边界校验，并确认至少存在一个可表示的未来发生时点。
// 失败一律返回 `invalid_schedule`（Q3 已定：沿用该码，不新增细分码），消息带可读原因。
func validateRRule(expr string) error {
	spec, err := parseRRule(expr)
	if err != nil {
		return err
	}
	// 至少存在一个未来时点：从 DTSTART 起找第一个 >= DTSTART 的发生时点。
	if _, ok, err := spec.nextOccurrence(spec.start.Add(-time.Second)); err != nil {
		return err
	} else if !ok {
		return rruleErr("该 rrule 没有任何可表示的发生时点（规则可能永远选不中日期）")
	}
	return nil
}

// rruleNextAfter 返回严格晚于 t 的下一次发生时点。
func rruleNextAfter(expr string, t time.Time) (Occurrence, bool, error) {
	spec, err := parseRRule(expr)
	if err != nil {
		return Occurrence{}, false, err
	}
	return spec.nextOccurrence(t)
}

// rruleLatestAtOrBefore 返回不晚于 t 的最近一次发生时点（停机只补最近一次用）。
func rruleLatestAtOrBefore(expr string, now time.Time) (Occurrence, bool, error) {
	spec, err := parseRRule(expr)
	if err != nil {
		return Occurrence{}, false, err
	}
	return spec.latestAtOrBefore(now)
}

// ——— 解析 ———

func rruleErr(msg string) error {
	return failure.NewError(failure.ClassRequest, "invalid_schedule", "rrule 非法："+msg, nil)
}

func parseRRule(expr string) (*rruleSpec, error) {
	startLine, ruleLine, err := rruleLines(expr)
	if err != nil {
		return nil, err
	}
	loc, startWall, startUTC, err := rruleParseStart(startLine)
	if err != nil {
		return nil, err
	}
	// DTSTART 必须是存在的本地时刻（再落绝对时刻验证一次）。
	startAbs, ok := dstResolve(startWall, loc)
	if !ok {
		return nil, rruleErr("DTSTART 在其时区里不是真实存在的本地时刻（落入夏令时空档）")
	}
	parts, order, err := rruleRuleParts(ruleLine)
	if err != nil {
		return nil, err
	}
	spec := &rruleSpec{norm: startLine + "\n" + ruleLine, loc: loc, start: startAbs, startWall: startWall, startUTC: startUTC, wkst: 0}
	if err := spec.decodeValues(parts, order); err != nil {
		return nil, err
	}
	if err := spec.checkCombination(); err != nil {
		return nil, err
	}
	if err := spec.applyDefaults(); err != nil {
		return nil, err
	}
	if err := spec.checkCost(); err != nil {
		return nil, err
	}
	// limitWall：UNTIL 或年份上限。
	if spec.hasUntil {
		// UNTIL 是绝对时刻；换算到本地墙钟朴素时间作为扫描上界（多留一天余量）。
		spec.limitWall = wallIn(spec.until, loc).AddDate(0, 0, 1)
	} else {
		spec.limitWall = time.Date(maxRRuleYear, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	return spec, nil
}

// rruleLines 切出 DTSTART 与 RRULE 两行（拒绝多余行/顺序错）。
func rruleLines(value string) (string, string, error) {
	raw := strings.ReplaceAll(value, "\r\n", "\n")
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", rruleErr("为空（需要一行 DTSTART 加一行 RRULE）")
	}
	var lines []string
	for _, ln := range strings.Split(trimmed, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) != 2 {
		return "", "", rruleErr("必须恰好两行：一行 DTSTART、一行 RRULE")
	}
	if !strings.HasPrefix(lines[0], "DTSTART") || !strings.HasPrefix(lines[1], "RRULE:") {
		return "", "", rruleErr("第一行须是 DTSTART、第二行须是 RRULE:")
	}
	return lines[0], lines[1], nil
}

// rruleParseStart 解析 DTSTART：`DTSTART:YYYYMMDDTHHMMSSZ` 或 `DTSTART;TZID=Zone:YYYYMMDDTHHMMSS`。
func rruleParseStart(line string) (*time.Location, time.Time, bool, error) {
	if rest, ok := strings.CutPrefix(line, "DTSTART:"); ok {
		wall, err := rruleBasicTime("DTSTART", rest, true)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		return time.UTC, wall, true, nil
	}
	if rest, ok := strings.CutPrefix(line, "DTSTART;TZID="); ok {
		colon := strings.LastIndex(rest, ":")
		if colon <= 0 {
			return nil, time.Time{}, false, rruleErr("DTSTART;TZID 形式应为 DTSTART;TZID=Area/Location:YYYYMMDDTHHMMSS")
		}
		zone := rest[:colon]
		body := rest[colon+1:]
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, time.Time{}, false, rruleErr("DTSTART 的 TZID 不是合法 IANA 时区：" + zone)
		}
		wall, err := rruleBasicTime("DTSTART", body, false)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		return loc, wall, false, nil
	}
	return nil, time.Time{}, false, rruleErr("DTSTART 必须用 UTC（…Z）或显式 TZID=IANA 本地时间")
}

// rruleBasicTime 解析 `YYYYMMDDTHHMMSS[Z]` 为"本地墙钟朴素时间"（用 UTC location 承载年月日时分秒）。
// requireZ 为 true 时要求结尾有 Z；为 false 时不允许有 Z。
func rruleBasicTime(field, text string, requireZ bool) (time.Time, error) {
	hasZ := strings.HasSuffix(text, "Z")
	if requireZ && !hasZ {
		return time.Time{}, rruleErr(field + " 的 UTC 形态必须以 Z 结尾")
	}
	if !requireZ && hasZ {
		return time.Time{}, rruleErr(field + " 使用 TZID 时不能带 Z 后缀")
	}
	body := strings.TrimSuffix(text, "Z")
	layout := "20060102T150405"
	if len(body) != len(layout) {
		return time.Time{}, rruleErr(field + " 必须是真实的 YYYYMMDDTHHMMSS 日期时间：" + text)
	}
	v, err := time.ParseInLocation(layout, body, time.UTC)
	if err != nil {
		return time.Time{}, rruleErr(field + " 不是合法的日历日期时间：" + text)
	}
	if v.Year() < minRRuleYear || v.Year() > maxRRuleYear {
		return time.Time{}, rruleErr(fmt.Sprintf("%s 年份需在 %d–%d", field, minRRuleYear, maxRRuleYear))
	}
	return v, nil
}

// rruleRuleParts 把 RRULE 行拆成 key→value（保留出现顺序，检查唯一性）。
func rruleRuleParts(line string) (map[string]string, []string, error) {
	parts := make(map[string]string)
	var order []string
	body := strings.TrimPrefix(line, "RRULE:")
	if body == "" {
		return nil, nil, rruleErr("RRULE 不能为空")
	}
	for _, comp := range strings.Split(body, ";") {
		eq := strings.Index(comp, "=")
		if eq <= 0 || eq == len(comp)-1 {
			return nil, nil, rruleErr("RRULE 字段必须是 KEY=VALUE 且值非空")
		}
		key := comp[:eq]
		val := comp[eq+1:]
		if _, dup := parts[key]; dup {
			return nil, nil, rruleErr("RRULE 字段名重复：" + key)
		}
		ok := false
		for _, s := range rruleSupportedParts {
			if s == key {
				ok = true
				break
			}
		}
		if !ok {
			return nil, nil, rruleErr("RRULE 只接受 " + strings.Join(rruleSupportedParts, "/") + "，得到 " + key)
		}
		parts[key] = val
		order = append(order, key)
	}
	return parts, order, nil
}

func (spec *rruleSpec) decodeValues(parts map[string]string, order []string) error {
	freq, ok := parts["FREQ"]
	if !ok {
		return rruleErr("缺少 FREQ")
	}
	if freq == "SECONDLY" {
		return rruleErr("FREQ=SECONDLY 不支持（相邻发生时点至少间隔 60 秒）")
	}
	valid := false
	for _, f := range rruleFrequencies {
		if f == freq {
			valid = true
			break
		}
	}
	if !valid {
		return rruleErr("FREQ 必须是 " + strings.Join(rruleFrequencies, "/") + "，得到 " + freq)
	}
	spec.freq = freq

	if v, ok := parts["INTERVAL"]; ok {
		n, err := rrulePositiveInt(v)
		if err != nil {
			return rruleErr("INTERVAL 必须是正整数，得到 " + v)
		}
		spec.interval = n
	} else {
		spec.interval = 1
	}
	if v, ok := parts["COUNT"]; ok {
		n, err := rrulePositiveInt(v)
		if err != nil || n < 1 || n > maxRRuleCount {
			return rruleErr(fmt.Sprintf("COUNT 必须是 1–%d 的整数（更长请用 UNTIL）", maxRRuleCount))
		}
		spec.count = n
	}
	if v, ok := parts["UNTIL"]; ok {
		u, err := rruleBasicTime("UNTIL", v, true)
		if err != nil {
			return err
		}
		spec.until = u
		spec.hasUntil = true
	}
	if v, ok := parts["WKST"]; ok {
		idx := -1
		for i, w := range rruleWeekdays {
			if w == v {
				idx = i
				break
			}
		}
		if idx < 0 {
			return rruleErr("WKST 必须是 MO/TU/WE/TH/FR/SA/SU")
		}
		spec.wkst = idx
	}
	var err error
	if spec.bySecond, err = spec.decodeList(parts, "BYSECOND"); err != nil {
		return err
	}
	if spec.byMinute, err = spec.decodeList(parts, "BYMINUTE"); err != nil {
		return err
	}
	if spec.byHour, err = spec.decodeList(parts, "BYHOUR"); err != nil {
		return err
	}
	if spec.byMonthDay, err = spec.decodeList(parts, "BYMONTHDAY"); err != nil {
		return err
	}
	if spec.byYearDay, err = spec.decodeList(parts, "BYYEARDAY"); err != nil {
		return err
	}
	if spec.byWeekNo, err = spec.decodeList(parts, "BYWEEKNO"); err != nil {
		return err
	}
	if spec.byMonth, err = spec.decodeList(parts, "BYMONTH"); err != nil {
		return err
	}
	if spec.bySetPos, err = spec.decodeList(parts, "BYSETPOS"); err != nil {
		return err
	}
	if v, ok := parts["BYDAY"]; ok {
		if spec.byDay, err = rruleDecodeByDay(v); err != nil {
			return err
		}
	}
	return nil
}

func rrulePositiveInt(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not unsigned")
		}
	}
	return strconv.Atoi(s)
}

func (spec *rruleSpec) decodeList(parts map[string]string, key string) ([]int, error) {
	text, ok := parts[key]
	if !ok {
		return nil, nil
	}
	rng := rruleListRanges[key]
	signed := rng.min < 0
	seen := make(map[int]bool)
	var out []int
	for _, item := range strings.Split(text, ",") {
		n, err := strconv.Atoi(item)
		if err != nil {
			return nil, rruleErr(key + " 含非整数值：" + item)
		}
		if !signed && strings.HasPrefix(item, "-") {
			return nil, rruleErr(key + " 不接受负数：" + item)
		}
		if signed && strings.HasPrefix(item, "+") {
			return nil, rruleErr(key + " 不接受 + 号：" + item)
		}
		if n < rng.min || n > rng.max || (signed && n == 0) {
			if signed {
				return nil, rruleErr(fmt.Sprintf("%s 取值需在 %d–%d（不含 0）", key, rng.min, rng.max))
			}
			return nil, rruleErr(fmt.Sprintf("%s 取值需在 %d–%d", key, rng.min, rng.max))
		}
		if seen[n] {
			return nil, rruleErr(key + " 取值不能重复")
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

func rruleDecodeByDay(text string) ([]byDayEntry, error) {
	seen := make(map[string]bool)
	var out []byDayEntry
	for _, sel := range strings.Split(text, ",") {
		if len(sel) < 2 {
			return nil, rruleErr("BYDAY 含非法星期选择子：" + sel)
		}
		wd := sel[len(sel)-2:]
		wdIdx := -1
		for i, w := range rruleWeekdays {
			if w == wd {
				wdIdx = i
				break
			}
		}
		if wdIdx < 0 {
			return nil, rruleErr("BYDAY 的星期必须是 MO/TU/WE/TH/FR/SA/SU：" + sel)
		}
		ordText := sel[:len(sel)-2]
		ordinal := 0
		if ordText != "" {
			n, err := strconv.Atoi(ordText)
			if err != nil || n == 0 || n < -53 || n > 53 {
				return nil, rruleErr("BYDAY 序数需在 ±1–±53：" + sel)
			}
			ordinal = n
		}
		key := fmt.Sprintf("%d%s", ordinal, wd)
		if seen[key] {
			return nil, rruleErr("BYDAY 取值不能重复")
		}
		seen[key] = true
		out = append(out, byDayEntry{ordinal: ordinal, weekday: wdIdx})
	}
	return out, nil
}

// checkCombination 拒绝 RFC 5545 禁止或破坏 60 秒间距的组合（照抄 rrule.ts checkCombination）。
func (spec *rruleSpec) checkCombination() error {
	if spec.count > 0 && spec.hasUntil {
		return rruleErr("不能同时含 COUNT 与 UNTIL")
	}
	if len(spec.bySecond) > 1 {
		return rruleErr("BYSECOND 只能有一个值（相邻发生时点至少间隔 60 秒）")
	}
	hasBy := len(spec.bySecond)+len(spec.byMinute)+len(spec.byHour)+len(spec.byMonthDay)+
		len(spec.byYearDay)+len(spec.byWeekNo)+len(spec.byMonth)+len(spec.bySetPos) > 0 || len(spec.byDay) > 0
	if spec.freq == "MINUTELY" || spec.freq == "HOURLY" {
		if hasBy {
			return rruleErr("FREQ=" + spec.freq + " 不支持 BY 部分（请用 FREQ=DAILY/WEEKLY 配 BYHOUR/BYMINUTE）")
		}
	}
	if (len(spec.byWeekNo) > 0 || len(spec.byYearDay) > 0) && spec.freq != "YEARLY" {
		return rruleErr("BYWEEKNO/BYYEARDAY 需 FREQ=YEARLY")
	}
	if len(spec.byMonthDay) > 0 && spec.freq != "MONTHLY" && spec.freq != "YEARLY" {
		return rruleErr("BYMONTHDAY 需 FREQ=MONTHLY 或 YEARLY")
	}
	ordinals := 0
	for _, d := range spec.byDay {
		if d.ordinal != 0 {
			ordinals++
		}
	}
	if ordinals > 0 && spec.freq != "MONTHLY" && spec.freq != "YEARLY" {
		return rruleErr("带序数的 BYDAY 需 FREQ=MONTHLY 或 YEARLY")
	}
	if ordinals > 0 && len(spec.byWeekNo) > 0 {
		return rruleErr("带序数的 BYDAY 不能与 BYWEEKNO 组合")
	}
	if len(spec.bySetPos) > 0 && !hasOtherBy(spec) {
		return rruleErr("BYSETPOS 必须搭配另一项 BY")
	}
	return nil
}

func hasOtherBy(spec *rruleSpec) bool {
	return len(spec.bySecond)+len(spec.byMinute)+len(spec.byHour)+len(spec.byMonthDay)+
		len(spec.byYearDay)+len(spec.byWeekNo)+len(spec.byMonth)+len(spec.byDay) > 0
}

// applyDefaults 从 DTSTART 补时间默认值（DAILY 及更长频率遗漏时），与 rrule.ts startDefaults 对齐。
func (spec *rruleSpec) applyDefaults() error {
	if spec.freq == "MINUTELY" || spec.freq == "HOURLY" {
		return nil
	}
	sw := spec.startWall
	if len(spec.byHour) == 0 {
		spec.byHour = []int{sw.Hour()}
	}
	if len(spec.byMinute) == 0 {
		spec.byMinute = []int{sw.Minute()}
	}
	if len(spec.bySecond) == 0 {
		spec.bySecond = []int{sw.Second()}
	}
	if len(spec.byDay) > 0 || len(spec.byMonthDay) > 0 || len(spec.byYearDay) > 0 || len(spec.byWeekNo) > 0 {
		return nil
	}
	// Go time.Weekday: Sunday=0. rruleWeekdays: Monday=0. 转换。
	if spec.freq == "WEEKLY" {
		spec.byDay = []byDayEntry{{ordinal: 0, weekday: goWeekdayToRRule(int(sw.Weekday()))}}
	}
	if spec.freq == "MONTHLY" || spec.freq == "YEARLY" {
		spec.byMonthDay = []int{sw.Day()}
	}
	if spec.freq == "YEARLY" && len(spec.byMonth) == 0 {
		spec.byMonth = []int{int(sw.Month())}
	}
	return nil
}

func goWeekdayToRRule(goWd int) int {
	// Go: Sun=0..Sat=6 → rrule: Mon=0..Sun=6
	return (goWd + 6) % 7
}

// checkCost 拒绝一个 FREQ 周期内候选超上限的规则（density）。
func (spec *rruleSpec) checkCost() error {
	days := spec.periodDays()
	times := len(spec.byHour) * len(spec.byMinute) * len(spec.bySecond)
	if times < 1 {
		times = 1
	}
	candidates := days * times
	if candidates > maxPeriodCandidates {
		return rruleErr(fmt.Sprintf("一个 FREQ 周期最多可产生 %d 个发生时点，超过上限 %d", candidates, maxPeriodCandidates))
	}
	if len(spec.bySetPos) > maxSetPositions {
		return rruleErr(fmt.Sprintf("BYSETPOS 至多 %d 个值", maxSetPositions))
	}
	for _, p := range spec.bySetPos {
		if absInt(p) > candidates {
			return rruleErr(fmt.Sprintf("BYSETPOS 值需在 ±1–±%d（一个周期最多 %d 个发生时点）", candidates, candidates))
		}
	}
	return nil
}

// periodDays 一个 FREQ 周期最多可能选中的天数（对齐 rrule.ts periodDays）。
func (spec *rruleSpec) periodDays() int {
	size := func(list []int) int {
		if len(list) == 0 {
			return -1 // 表示 ∞
		}
		return len(list)
	}
	min := func(vals ...int) int {
		res := -1
		for _, v := range vals {
			if v < 0 {
				continue
			}
			if res < 0 || v < res {
				res = v
			}
		}
		if res < 0 {
			return -1
		}
		return res
	}
	byDayDays := func(perWeekday int) int {
		if len(spec.byDay) == 0 {
			return -1
		}
		ordinals := 0
		for _, d := range spec.byDay {
			if d.ordinal != 0 {
				ordinals++
			}
		}
		return ordinals + perWeekday*(len(spec.byDay)-ordinals)
	}
	switch spec.freq {
	case "MINUTELY", "HOURLY", "DAILY":
		return 1
	case "WEEKLY":
		return min(7, byDayDays(1))
	case "MONTHLY":
		return min(31, size(spec.byMonthDay), byDayDays(5))
	case "YEARLY":
		months := size(spec.byMonth)
		if months < 0 {
			return min(366, size(spec.byYearDay), mulOrInf(12, size(spec.byMonthDay)), mulOrInf(9, size(spec.byWeekNo)), byDayDays(53))
		}
		return min(mulOrInf(31, months), size(spec.byYearDay), mulOrInf(months, size(spec.byMonthDay)),
			mulOrInf(9, size(spec.byWeekNo)), mulOrInf(months, byDayDays(5)))
	}
	return 1
}

func mulOrInf(a, b int) int {
	if a < 0 || b < 0 {
		return -1
	}
	return a * b
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ——— 枚举 ———

// nextOccurrence 返回严格晚于 after 的第一个发生时点。
func (spec *rruleSpec) nextOccurrence(after time.Time) (Occurrence, bool, error) {
	occ, ok, err := spec.find(after, false)
	if err != nil || !ok {
		return Occurrence{}, ok, err
	}
	return spec.makeOccurrence(occ), true, nil
}

// latestAtOrBefore 返回不晚于 now 的最近一个发生时点。
func (spec *rruleSpec) latestAtOrBefore(now time.Time) (Occurrence, bool, error) {
	occ, ok, err := spec.find(now, true)
	if err != nil || !ok {
		return Occurrence{}, ok, err
	}
	return spec.makeOccurrence(occ), true, nil
}

func (spec *rruleSpec) makeOccurrence(abs time.Time) Occurrence {
	wall := abs.In(spec.loc)
	return Occurrence{At: abs, Wall: wall.Format(WallFormat), Timezone: spec.loc.String()}
}

// find 用朴素墙钟按 FREQ 步进枚举候选，找到"严格晚于/不晚于 t"的绝对发生时刻。
//
// includeEqual=true 时返回 ≤ t 的最近一个（latestAtOrBefore）；false 时返回 > t 的第一个（nextAfter）。
// 不存在的本地时刻跳过（不消耗 COUNT）；COUNT 计入"有效发生时点"。
func (spec *rruleSpec) find(t time.Time, includeEqual bool) (time.Time, bool, error) {
	// 扫描起点：从 DTSTART 的所在周期开始，但不早于 count 需要枚举的起点。
	// 决策墙钟：t 在 spec.loc 里的墙钟朴素时间。
	decisionWall := wallIn(t, spec.loc)

	var countSoFar int
	var bestMatch time.Time
	var haveBest bool

	// 枚举墙钟候选（升序），逐个解析为绝对时刻。
	emit := func(wallNaive time.Time) (stop bool) {
		abs, ok := dstResolve(wallNaive, spec.loc)
		if !ok {
			return false // 不存在的本地时刻：跳过，不消耗 COUNT
		}
		// UNTIL 上界（绝对时刻）。
		if spec.hasUntil && abs.After(spec.until) {
			return true
		}
		// COUNT 计数：只在 abs > DTSTART 之后（含 DTSTART 本身算第 1 个）计入。
		if !abs.Before(spec.start) {
			countSoFar++
			if spec.count > 0 && countSoFar > spec.count {
				return true
			}
		}
		if includeEqual {
			if !abs.After(t) {
				bestMatch = abs
				haveBest = true
				return false
			}
			return true // 已越过 t，最近一个已记录
		}
		// nextAfter：严格晚于 t
		if abs.After(t) {
			bestMatch = abs
			haveBest = true
			return true
		}
		return false
	}

	visited := 0
	yieldErr := spec.enumerate(decisionWall, includeEqual, func(wallNaive time.Time) bool {
		visited++
		if visited > maxRRuleSearchOccurs {
			return true // 硬上限，防病态
		}
		return emit(wallNaive)
	})
	if yieldErr != nil {
		return time.Time{}, false, yieldErr
	}
	if !haveBest {
		// latestAtOrBefore 时，若 t 早于 DTSTART，则无（返回 false）。
		return time.Time{}, false, nil
	}
	return bestMatch, true, nil
}

// enumerate 按 FREQ 步进，从 decisionWall 附近的周期起，向未来枚举墙钟候选（升序）。
//
// includeEqual 决定起始周期是否要往前多回看（latestAtOrBefore 需要回看到 ≤ decisionWall 的最近一个；
// nextAfter 只需从 decisionWall 所在周期开始往后）。为兼顾两者，这里总是从"覆盖 decisionWall 的周期"
// 起步，并**若 includeEqual 且本周期候选全部 > t**，再向前回退一个周期窗口去找最近的 ≤ t 候选。
//
// 但因为本函数按增量枚举、不知道哪天是"最近的一次"，对 includeEqual 采用更简单可靠的做法：
// 由 find 的调用者（latestAtOrBefore）传入 includeEqual=true 时，我们从 DTSTART 起正向枚举直到越过 t
// ——低频规则（用户级定时任务）枚举量可控；而 nextAfter 从 decisionWall 附近起步向后枚举。
func (spec *rruleSpec) enumerate(decisionWall time.Time, includeEqual bool, yield func(time.Time) bool) error {
	// 有限规则（COUNT）必须从 DTSTART 起枚举，才能正确计入第 N 个有效发生时点后停止——
	// 否则从 decisionWall 附近起枚举会让 COUNT 计数从 0 重来，永不耗尽。
	if includeEqual || spec.count > 0 {
		return spec.enumerateFrom(spec.startWall, yield)
	}
	// nextAfter：从 decisionWall 所在周期的起点（不早于 startWall）开始，向后枚举。
	startWall := decisionWall
	if startWall.Before(spec.startWall) {
		startWall = spec.startWall
	}
	return spec.enumerateFrom(startWall, yield)
}

// enumerateFrom 从 fromWall 的周期起，逐周期向未来枚举候选墙钟时刻（升序），yield 返回 true 停止。
func (spec *rruleSpec) enumerateFrom(fromWall time.Time, yield func(time.Time) bool) error {
	periodStart := spec.periodBoundary(fromWall)
	if periodStart.Before(spec.startWall) {
		periodStart = spec.periodBoundary(spec.startWall)
	}
	// 迭代周期，最多到 limitWall 或硬上限。
	for iter := 0; iter < maxRRuleSearchOccurs; iter++ {
		if periodStart.After(spec.limitWall) {
			return nil
		}
		cands := spec.candidatesInPeriod(periodStart)
		for _, c := range cands {
			if c.Before(spec.startWall) {
				continue // 早于 DTSTART 的发生时点不计
			}
			if yield(c) {
				return nil
			}
		}
		nextPeriod := spec.advancePeriod(periodStart)
		if !nextPeriod.After(periodStart) {
			return rruleErr("rrule 周期推进停滞（规则异常）")
		}
		periodStart = nextPeriod
	}
	return nil
}

// periodBoundary 返回覆盖 wall 的那个 FREQ 周期起点（对齐 DTSTART 的 INTERVAL 对齐），墙钟朴素时间。
func (spec *rruleSpec) periodBoundary(wall time.Time) time.Time {
	s := spec.startWall
	switch spec.freq {
	case "MINUTELY":
		return floorTimeUnit(s, wall, spec.interval, time.Minute)
	case "HOURLY":
		return floorTimeUnit(s, wall, spec.interval, time.Hour)
	case "DAILY":
		days := int(floorFloat(float64(daysBetween(s, wall)) / float64(spec.interval)))
		return time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, days*spec.interval)
	case "WEEKLY":
		ws := weekStartOfDay(s, spec.wkst)
		ww := weekStartOfDay(wall, spec.wkst)
		weeks := int(floorFloat(float64(daysBetween(ws, ww)) / float64(7*spec.interval)))
		return time.Date(ws.Year(), ws.Month(), ws.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, weeks*7*spec.interval)
	case "MONTHLY":
		months := monthIndex(wall) - monthIndex(s)
		k := int(floorFloat(float64(months) / float64(spec.interval)))
		mi := monthIndex(s) + k*spec.interval
		return time.Date(mi/12, time.Month(mi%12+1), 1, 0, 0, 0, 0, time.UTC)
	case "YEARLY":
		years := wall.Year() - s.Year()
		k := int(floorFloat(float64(years) / float64(spec.interval)))
		return time.Date(s.Year()+k*spec.interval, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return spec.startWall
}

// advancePeriod 返回下一个 FREQ 周期起点。
func (spec *rruleSpec) advancePeriod(periodStart time.Time) time.Time {
	switch spec.freq {
	case "MINUTELY":
		return periodStart.Add(time.Duration(spec.interval) * time.Minute)
	case "HOURLY":
		return periodStart.Add(time.Duration(spec.interval) * time.Hour)
	case "DAILY":
		return periodStart.AddDate(0, 0, spec.interval)
	case "WEEKLY":
		return periodStart.AddDate(0, 0, 7*spec.interval)
	case "MONTHLY":
		return periodStart.AddDate(0, spec.interval, 0)
	case "YEARLY":
		return periodStart.AddDate(spec.interval, 0, 0)
	}
	return periodStart.AddDate(0, 0, 1)
}

// candidatesInPeriod 返回一个 FREQ 周期内所有候选墙钟时刻（升序），已应用 BY* 过滤与 BYSETPOS。
func (spec *rruleSpec) candidatesInPeriod(periodStart time.Time) []time.Time {
	switch spec.freq {
	case "MINUTELY", "HOURLY":
		// 时间列表默认已由 applyDefaults 补齐（MINUTELY/HOURLY 不做默认，用 DTSTART 的秒/分/时）。
		return spec.subDailyCandidates(periodStart)
	case "DAILY":
		day := time.Date(periodStart.Year(), periodStart.Month(), periodStart.Day(), 0, 0, 0, 0, time.UTC)
		return spec.dayTimeCandidates(day)
	case "WEEKLY":
		var out []time.Time
		for d := 0; d < 7; d++ {
			day := time.Date(periodStart.Year(), periodStart.Month(), periodStart.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, d)
			if !spec.dayMatchesByWeek(day) {
				continue
			}
			out = append(out, spec.dayTimeCandidates(day)...)
		}
		return out
	case "MONTHLY":
		var out []time.Time
		days := spec.monthMatchingDays(periodStart.Year(), int(periodStart.Month()))
		for _, day := range days {
			out = append(out, spec.dayTimeCandidates(day)...)
		}
		return out
	case "YEARLY":
		var out []time.Time
		days := spec.yearMatchingDays(periodStart.Year())
		for _, day := range days {
			out = append(out, spec.dayTimeCandidates(day)...)
		}
		return out
	}
	return nil
}

// subDailyCandidates MINUTELY/HOURLY：周期起点 + 默认时间（DTSTART 的时/分/秒）。
func (spec *rruleSpec) subDailyCandidates(periodStart time.Time) []time.Time {
	if spec.freq == "MINUTELY" {
		// 每分钟一个；秒取 DTSTART 秒（applyDefaults 不补，用 DTSTART 的）。
		return []time.Time{time.Date(periodStart.Year(), periodStart.Month(), periodStart.Day(),
			periodStart.Hour(), periodStart.Minute(), spec.startWall.Second(), 0, time.UTC)}
	}
	// HOURLY：小时起点 + DTSTART 分/秒。
	return []time.Time{time.Date(periodStart.Year(), periodStart.Month(), periodStart.Day(),
		periodStart.Hour(), spec.startWall.Minute(), spec.startWall.Second(), 0, time.UTC)}
}

// dayTimeCandidates 某天 × BYHOUR × BYMINUTE × BYSECOND，按时间升序。
func (spec *rruleSpec) dayTimeCandidates(day time.Time) []time.Time {
	hours := sortedInts(spec.byHour)
	mins := sortedInts(spec.byMinute)
	secs := sortedInts(spec.bySecond)
	var out []time.Time
	for _, h := range hours {
		for _, m := range mins {
			for _, sec := range secs {
				out = append(out, time.Date(day.Year(), day.Month(), day.Day(), h, m, sec, 0, time.UTC))
			}
		}
	}
	return out
}

// dayMatchesByWeek WEEKLY 频率下某天是否命中 BYDAY（无序号）。
func (spec *rruleSpec) dayMatchesByWeek(day time.Time) bool {
	if len(spec.byDay) == 0 {
		return true
	}
	wd := goWeekdayToRRule(int(day.Weekday()))
	for _, d := range spec.byDay {
		if d.ordinal == 0 && d.weekday == wd {
			return true
		}
	}
	return false
}

// monthMatchingDays 某月命中 BYMONTHDAY / BYDAY(含序数) 的日期（已排序去重）。
func (spec *rruleSpec) monthMatchingDays(year, month int) []time.Time {
	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	// 1) BYMONTHDAY 过滤（无则全部）。
	mdays := map[int]bool{}
	if len(spec.byMonthDay) > 0 {
		for _, md := range spec.byMonthDay {
			var d int
			if md > 0 {
				d = md
			} else {
				d = daysInMonth + md + 1
			}
			if d >= 1 && d <= daysInMonth {
				mdays[d] = true
			}
		}
		if len(mdays) == 0 {
			return nil
		}
	}
	// 2) BYDAY 过滤（区分有无序数）。
	byDaySet := map[int]bool{}
	ordSet := map[int]bool{}
	hasOrdinal := false
	for _, d := range spec.byDay {
		if d.ordinal == 0 {
			byDaySet[d.weekday] = true
		} else {
			hasOrdinal = true
			ordSet[spec.nthWeekdayOfMonth(year, month, d.weekday, d.ordinal)] = true
		}
	}
	var out []time.Time
	for day := 1; day <= daysInMonth; day++ {
		dt := firstDay.AddDate(0, 0, day-1)
		if len(mdays) > 0 && !mdays[day] {
			continue
		}
		if len(spec.byDay) > 0 {
			wd := goWeekdayToRRule(int(dt.Weekday()))
			match := false
			if byDaySet[wd] {
				match = true
			}
			if hasOrdinal && ordSet[day] {
				match = true
			}
			if !match {
				continue
			}
		}
		out = append(out, dt)
	}
	return out
}

// nthWeekdayOfMonth 返回某月第 n 个（n>0）或倒数第 |n| 个（n<0）指定星期的日号；不存在返回 0。
func (spec *rruleSpec) nthWeekdayOfMonth(year, month, weekday, n int) int {
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if n > 0 {
		// 第 n 个 wd：weekday 是 rrule 索引（Mon=0），first.Weekday() 是 Go 索引（Sun=0），统一成 rrule 索引比较。
		first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		firstRRule := goWeekdayToRRule(int(first.Weekday()))
		offset := (weekday - firstRRule + 7) % 7
		day := 1 + offset + (n-1)*7
		if day > daysInMonth {
			return 0
		}
		return day
	}
	last := time.Date(year, time.Month(month), daysInMonth, 0, 0, 0, 0, time.UTC)
	lastRRule := goWeekdayToRRule(int(last.Weekday()))
	offset := (lastRRule - weekday + 7) % 7
	day := daysInMonth - offset - (absInt(n)-1)*7
	if day < 1 {
		return 0
	}
	return day
}

// yearMatchingDays YEARLY 频率下某年命中所有 BY 规则的日期。
func (spec *rruleSpec) yearMatchingDays(year int) []time.Time {
	months := spec.byMonth
	if len(months) == 0 {
		months = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	}
	// BYYEARDAY / BYWEEKNO 复杂且少用；本有界子集实现它们的常见正/负日号，周号按 ISO 近似。
	var out []time.Time
	for _, m := range months {
		out = append(out, spec.monthMatchingDays(year, m)...)
	}
	// BYYEARDAY 过滤（若有）
	if len(spec.byYearDay) > 0 {
		yearStart := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		total := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Sub(yearStart).Hours() / 24
		keep := map[int]bool{}
		for _, yd := range spec.byYearDay {
			var d int
			if yd > 0 {
				d = yd
			} else {
				d = int(total) + yd + 1
			}
			if d >= 1 && d <= int(total) {
				keep[d] = true
			}
		}
		var filtered []time.Time
		for _, dt := range out {
			dayOfYear := int(dt.Sub(yearStart).Hours()/24) + 1
			if keep[dayOfYear] {
				filtered = append(filtered, dt)
			}
		}
		out = filtered
	}
	return out
}

// ——— 时间工具 ———

// wallIn 返回 t 在 loc 里的"墙钟朴素时间"（用 UTC location 承载年月日时分秒）。
func wallIn(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// dstResolve 把墙钟朴素时间解析为 loc 里的绝对时刻：不存在的本地时刻返回 ok=false；
// 重叠（秋季回拨）取**较早**的发生时刻（与 dayCandidates 的 time.Date 行为一致）。
//
// 判定"不存在"的方法：用 time.Date 得到候选绝对时刻，再换算回 loc 读回墙钟；若墙钟与输入不一致，
// 说明该本地时刻不存在（空档）。这与 dayCandidates 里"回读校验"的认知一致。
func dstResolve(wall time.Time, loc *time.Location) (time.Time, bool) {
	abs := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
	back := abs.In(loc)
	if back.Year() != wall.Year() || back.Month() != wall.Month() || back.Day() != wall.Day() ||
		back.Hour() != wall.Hour() || back.Minute() != wall.Minute() || back.Second() != wall.Second() {
		return time.Time{}, false
	}
	// 重叠取较早：若存在更早的绝对时刻也能映射到同一墙钟，则取更早的。
	earlier := abs.Add(-time.Hour)
	if e := earlier.In(loc); e.Year() == wall.Year() && e.Month() == wall.Month() && e.Day() == wall.Day() &&
		e.Hour() == wall.Hour() && e.Minute() == wall.Minute() && e.Second() == wall.Second() {
		return earlier, true
	}
	return abs, true
}

func daysBetween(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

func weekStartOfDay(t time.Time, wkst int) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	// t.Weekday(): Sun=0..Sat=6 → rrule index Mon=0..Sun=6
	wd := goWeekdayToRRule(int(day.Weekday()))
	back := (wd - wkst + 7) % 7
	return day.AddDate(0, 0, -back)
}

func monthIndex(t time.Time) int { return t.Year()*12 + int(t.Month()) - 1 }

func floorTimeUnit(origin, value time.Time, interval int, unit time.Duration) time.Time {
	step := time.Duration(interval) * unit
	diff := value.Sub(origin)
	k := floorFloat(float64(diff) / float64(step))
	return origin.Add(time.Duration(k) * step)
}

func floorFloat(f float64) float64 {
	if f < 0 {
		return -float64(int(-f) + 1)
	}
	return float64(int(f))
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}
