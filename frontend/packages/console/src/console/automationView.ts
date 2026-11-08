import type {
  Automation,
  AutomationPreviewItem,
  AutomationRunRecord,
  AutomationRunStatus,
  AutomationSchedule,
  AutomationScheduleKind,
  AutomationState,
} from '@ark-mate/types'

import { fmtLocalInZone, fmtShortDateTime } from './time.ts'

// ——— automation 视图（M4-6）：纯函数，全部可在 typecheck 下穷举 ———
//
// 唯一一个真正需要"翻译"的地方是 schedule：时区 + DST + weekly 星期集合叠在一起，
// 没人能从 JSON 里读出「到底什么时候跑」。所以这里既给一句人话，也原样回带文件里的值
// （面板不替用户解释、不归一，归一由服务端装载期做并回写）。

export const KIND_OPTIONS: { label: string; value: AutomationScheduleKind }[] = [
  { label: '一次（once）', value: 'once' },
  { label: '间隔（interval）', value: 'interval' },
  { label: '每天（daily）', value: 'daily' },
  { label: '每周（weekly）', value: 'weekly' },
  { label: '每月（monthly）', value: 'monthly' },
  { label: '重复规则（rrule）', value: 'rrule' },
]

/** 0=周日，与 Go `time.Weekday` / 后端 `days_of_week` 对齐。 */
export const WEEKDAYS: { label: string; value: number }[] = [
  { label: '周日', value: 0 },
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
]

/** 常用时区（显式选择；后端要求 IANA 名，猜本地时区是 bug 的来源）。 */
export const TIMEZONES: string[] = [
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'UTC',
]

/** 浏览器时区（进编辑器时的缺省值；用户可改）。 */
export function localTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

const pad = (n: number): string => String(n).padStart(2, '0')

/** `HH:MM`（缺省 09:00）。 */
export function hhmm(hour?: number, minute?: number): string {
  return `${pad(hour ?? 9)}:${pad(minute ?? 0)}`
}

/** 星期集合 → "周一、周三"（空 → "未选"；后端要求至少一天）。 */
export function weekdayText(days?: number[]): string {
  if (!days || days.length === 0) return '未选'
  return [...days]
    .sort((a, b) => a - b)
    .map((d) => WEEKDAYS.find((w) => w.value === d)?.label ?? `周?(${d})`)
    .join('、')
}

/** schedule → 一句人话（用于列表主行）。 */
export function describeSchedule(s: AutomationSchedule): string {
  const tz = s.timezone || 'UTC'
  switch (s.kind) {
    case 'once':
      return `一次 · ${s.at ? fmtLocalInZone(s.at, tz) : '未设时间'}（${tz}）`
    case 'interval':
      return `每 ${everySecondsText(s.every_seconds)}（与墙钟无关）`
    case 'weekly':
      return `每周 ${weekdayText(s.days_of_week)} ${hhmm(s.hour, s.minute)}（${tz}）`
    case 'monthly':
      return `每月 ${s.day ?? 1} 日 ${hhmm(s.hour, s.minute)}（${tz}）`
    case 'rrule':
      return s.rrule ? `重复规则 · ${rruleSummary(s.rrule)}` : '重复规则 · 未填写'
    default:
      return `每天 ${hhmm(s.hour, s.minute)}（${tz}）`
  }
}

/** 间隔秒数 → "N 秒 / N 分钟 / N 小时"（≥60，后端约束）。 */
export function everySecondsText(sec?: number): string {
  const n = sec ?? 3600
  if (n % 3600 === 0) return `${n / 3600} 小时`
  if (n % 60 === 0) return `${n / 60} 分钟`
  return `${n} 秒`
}

/** rrule 文本 → 一句摘要（列表主行用）：取 RRULE 行、突出 FREQ，剩余作为补充。 */
export function rruleSummary(rrule?: string): string {
  if (!rrule) return '未填写'
  const rule = rrule
    .split('\n')
    .map((l) => l.trim())
    .find((l) => l.startsWith('RRULE:'))
  if (!rule) return '缺少 RRULE 行'
  const body = rule.slice('RRULE:'.length)
  const parts = body.split(';').filter(Boolean)
  const freq = parts.find((p) => p.startsWith('FREQ='))?.slice('FREQ='.length)
  const rest = parts.filter((p) => !p.startsWith('FREQ='))
  const head = freq ? FREQ_LABEL[freq] ?? freq : body
  return rest.length > 0 ? `${head}（${rest.join(';')}）` : head
}

const FREQ_LABEL: Record<string, string> = {
  MINUTELY: '每分',
  HOURLY: '每小时',
  DAILY: '每天',
  WEEKLY: '每周',
  MONTHLY: '每月',
  YEARLY: '每年',
}

/** 状态标（颜色取自主题 token 的语义名，渲染侧再取色）。 */
export const STATE_META: Record<AutomationState, { label: string; tone: 'success' | 'muted' | 'warning' | 'danger' }> = {
  active: { label: '启用中', tone: 'success' },
  inactive: { label: '已停用', tone: 'muted' },
  archived: { label: '已归档', tone: 'muted' },
  needs_attention: { label: '需处理', tone: 'danger' },
}

/** 一次触发的结论标：**没跑成的那些也要看得见**（overlap/duplicate/skipped 不藏）。 */
export const RUN_STATUS_META: Record<
  AutomationRunStatus,
  { label: string; tone: 'success' | 'muted' | 'warning' | 'danger' }
> = {
  claimed: { label: '已触发', tone: 'success' },
  duplicate: { label: '重复（本次已触发过）', tone: 'muted' },
  overlap: { label: '跳过（上一次还在跑）', tone: 'warning' },
  skipped: { label: '未跑成', tone: 'danger' },
}

export const TRIGGER_LABEL: Record<string, string> = {
  scheduled: '按时',
  recovery: '停机补跑',
  manual: '手动',
}

/** 列表副行：最近一次的事实（状态 + 原因 + 时间），没有任何运行史时给一句实话。 */
export function lastRunText(a: Automation): string {
  if (a.error) return `运行配置有误：${a.error}`
  if (!a.last_status) return '尚未触发过'
  const meta = RUN_STATUS_META[a.last_status]
  const when = a.last_fired_at ? `（${fmtShortDateTime(a.last_fired_at)}）` : ''
  return `${meta.label}${when}${a.last_reason ? ` · ${a.last_reason}` : ''}`
}

/** `automation:<id>` → 是否由定时任务触发（会话打标用）。 */
export function isAutomationOrigin(origin?: string): boolean {
  return !!origin && origin.startsWith('automation:')
}

/** `automation:daily` → `daily`（打标文案用）。 */
export function automationIDFromOrigin(origin?: string): string {
  if (origin === undefined || !origin.startsWith('automation:')) return ''
  return origin.slice('automation:'.length)
}

/** 运行史时间轴的一行文案（状态 + 计划时刻 + 触发方式 + 原因）。 */
export function runLine(r: AutomationRunRecord): string {
  const parts = [RUN_STATUS_META[r.status].label, `计划 ${fmtShortDateTime(r.scheduled_for)}`]
  if (r.trigger) parts.push(TRIGGER_LABEL[r.trigger] ?? r.trigger)
  if (r.reason) parts.push(r.reason)
  return parts.join(' · ')
}

/** 预览项文案：墙钟（人看的）+ UTC（机器看的）。 */
export function previewLine(o: AutomationPreviewItem): string {
  return `${o.wall}（${o.timezone}） · ${fmtShortDateTime(o.at)}`
}

/**
 * 面板表单值 → 写面 body（**只带用户真的填了的键**）。
 *
 * `null` 在这里有意用于"这个键不要了"：`once` 清掉 `hour`/`minute`、`weekly` 清掉 `at`，
 * 否则换 kind 之后旧字段会留在文件里误导下一个读它的人（服务端把 `null` 当删键）。
 */
export interface ScheduleFormValues {
  kind: AutomationScheduleKind
  timezone: string
  /** once：`YYYY-MM-DDTHH:MM`（本地时间，转成绝对时刻落盘）。 */
  once_at?: string
  hour?: number
  minute?: number
  days_of_week?: number[]
  /** interval：每多少秒（≥60）。 */
  every_seconds?: number
  /** monthly：每月第几日（1–31）。 */
  day?: number
  /** rrule：两行文本 `DTSTART…\nRRULE:…`（时区取自 DTSTART）。 */
  rrule?: string
}

/** 表单值 → schedule 对象（含 null 删键）。`once_at` 按**所选时区**解释成绝对时刻。 */
export function scheduleFromForm(v: ScheduleFormValues): Record<string, unknown> {
  const out: Record<string, unknown> = { kind: v.kind }
  // rrule 自带时区（DTSTART 内），不强制 timezone；其余五态需要 timezone。
  if (v.kind !== 'rrule') out.timezone = v.timezone
  switch (v.kind) {
    case 'once':
      out.at = v.once_at ? zonedToRFC3339(v.once_at, v.timezone) : null
      out.hour = null
      out.minute = null
      out.days_of_week = null
      out.every_seconds = null
      out.day = null
      out.rrule = null
      break
    case 'interval':
      out.at = null
      out.hour = null
      out.minute = null
      out.days_of_week = null
      // 后端约束 ≥60；表单侧兜底到 60（不是 0），避免造出必然被拒的请求。
      out.every_seconds = Math.max(60, v.every_seconds ?? 3600)
      out.day = null
      out.rrule = null
      break
    case 'rrule':
      out.at = null
      out.hour = null
      out.minute = null
      out.days_of_week = null
      out.every_seconds = null
      out.day = null
      // 权威文本原样透传（后端不规范化；空串交给后端报 invalid_schedule）。
      out.rrule = (v.rrule ?? '').trim()
      break
    case 'weekly':
      out.at = null
      out.hour = v.hour ?? 9
      out.minute = v.minute ?? 0
      out.days_of_week = [...(v.days_of_week ?? [])].sort((a, b) => a - b)
      out.every_seconds = null
      out.day = null
      out.rrule = null
      break
    case 'monthly':
      out.at = null
      out.hour = v.hour ?? 9
      out.minute = v.minute ?? 0
      out.days_of_week = null
      out.every_seconds = null
      // 1–31；越界兜底到 1，避免造出必然被拒的请求。
      out.day = Math.min(31, Math.max(1, v.day ?? 1))
      out.rrule = null
      break
    default:
      out.at = null
      out.hour = v.hour ?? 9
      out.minute = v.minute ?? 0
      out.days_of_week = null
      out.every_seconds = null
      out.day = null
      out.rrule = null
  }
  return out
}

/**
 * `YYYY-MM-DDTHH:MM` + IANA 时区 → RFC3339 绝对时刻。
 *
 * 做法：先当成 UTC 得到一个瞬时，再用 `Intl` 反查该时区在那一刻的墙钟偏移，两次修正。
 * 比引一个日期库便宜，且 DST 的"不存在/重复"由服务端的 `shiftPastWall` 兜底（预览会显形）。
 */
export function zonedToRFC3339(local: string, timezone: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(local)
  if (!m) return null
  const [, y, mo, d, h, mi] = m
  const asUTC = Date.UTC(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi))
  if (Number.isNaN(asUTC)) return null
  const offsetMs = tzOffsetMs(new Date(asUTC), timezone)
  const guess = new Date(asUTC - offsetMs)
  // 跨 DST 边界时第一次修正可能落在另一侧，用修正后的时刻再查一次偏移（幂等，最多两轮）
  const offsetMs2 = tzOffsetMs(guess, timezone)
  const final = offsetMs2 === offsetMs ? guess : new Date(asUTC - offsetMs2)
  return final.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** 某时刻在指定时区相对 UTC 的偏移（毫秒）。 */
function tzOffsetMs(at: Date, timezone: string): number {
  try {
    const dtf = new Intl.DateTimeFormat('en-US', {
      timeZone: timezone,
      hour12: false,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
    const parts = dtf.formatToParts(at)
    const get = (t: string): number => Number(parts.find((p) => p.type === t)?.value ?? '0')
    const asLocal = Date.UTC(
      get('year'),
      get('month') - 1,
      get('day'),
      get('hour') % 24,
      get('minute'),
      get('second'),
    )
    return asLocal - at.getTime()
  } catch {
    return 0
  }
}

/** RFC3339 → `YYYY-MM-DDTHH:MM`（按给定/浏览器时区，回填表单）。
 *
 * 入参是 `schedule.at`——**用户可编辑的定义载体**，不是接口时间戳口径（见 `console/time.ts` 的例外）。 */
export function toLocalInput(iso: string | undefined, timezone: string): string | undefined {
  if (!iso) return undefined
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return undefined
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone,
      hour12: false,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }).formatToParts(d)
    const get = (t: string): string => parts.find((p) => p.type === t)?.value ?? '00'
    return `${get('year')}-${get('month')}-${get('day')}T${get('hour')}:${get('minute')}`
  } catch {
    return undefined
  }
}

/**
 * 定义 → 编辑器表单值。
 *
 * v19：表单里没有 `agent_id` 了（原来那一格是"由哪个 agent 执行"的选择面）。定时任务触发时建的
 * 会话落在构建期固定的底座上；`target` 里剩 cwd / model / prompt 三件事，都是任务自己的事实。
 */
export function formFromAutomation(a: Automation): ScheduleFormValues & {
  id: string
  name: string
  enabled: boolean
  cwd: string
  model?: string
  prompt: string
  max_run_duration?: string
  delivery: 'session' | 'new_session'
  session_id?: string
} {
  return {
    id: a.id,
    name: a.name,
    enabled: a.enabled,
    kind: a.schedule.kind,
    timezone: a.schedule.timezone || localTimezone(),
    once_at: toLocalInput(a.schedule.at, a.schedule.timezone || localTimezone()),
    hour: a.schedule.hour ?? 9,
    minute: a.schedule.minute ?? 0,
    days_of_week: a.schedule.days_of_week ?? [],
    every_seconds: a.schedule.every_seconds ?? 3600,
    day: a.schedule.day ?? 1,
    rrule: a.schedule.rrule ?? '',
    cwd: a.target.cwd,
    model: a.target.model,
    prompt: a.target.prompt,
    max_run_duration: a.max_run_duration,
    // 空/缺省 → session（与后端 NormalizeDelivery 一致）。
    delivery: a.target.delivery === 'new_session' ? 'new_session' : 'session',
    session_id: a.target.session_id,
  }
}

/**
 * 写面 body：**只带用户填了的键**（`undefined` 不进 body，键的出现与否是 PUT 的语义）。
 *
 * `id` 只在新建时带（覆盖写时路径就是 id；带上也让服务端校验一次一致性）。
 *
 * **不带 `runtime_hint`**：底座由构建期固定，用户面没有这个选择面；服务端对带它的请求是 40001。
 * 老定义里若还残留这个键，服务端会在下次保存时自愈清除（只删这一个键）。
 */
export function writeBody(
  v: ScheduleFormValues & {
    id?: string
    name: string
    enabled: boolean
    cwd: string
    model?: string
    prompt: string
    max_run_duration?: string
    delivery?: 'session' | 'new_session'
    session_id?: string
  },
  opts: { create: boolean },
): Record<string, unknown> {
  const delivery = v.delivery === 'new_session' ? 'new_session' : 'session'
  const target: Record<string, unknown> = {
    prompt: v.prompt,
    delivery,
  }
  if (delivery === 'session') {
    if (v.session_id) target.session_id = v.session_id
  } else {
    // new_session：cwd 必填（后端要求绝对路径）。
    target.cwd = v.cwd
  }
  if (v.model) target.model = v.model
  const body: Record<string, unknown> = {
    name: v.name,
    enabled: v.enabled,
    schedule: scheduleFromForm(v),
    target,
  }
  if (opts.create && v.id) body.id = v.id
  if (v.max_run_duration) body.max_run_duration = v.max_run_duration
  return body
}

/** 预览请求体（编辑器里"预览"按钮用；count 固定 5，够看出规律）。 */
export function previewBody(v: ScheduleFormValues, count = 5): { schedule: Record<string, unknown>; count: number } {
  return { schedule: scheduleFromForm(v), count }
}
