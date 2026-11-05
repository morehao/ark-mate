import type { UnixMillis } from '@ark-mate/types'

// ——— 时间显示的唯一收口（纯函数，便于 Node 侧校验） ———
//
// 后端统一返回 Unix 毫秒（`UnixMillis`），这里只负责**按需格式化**：相对时间、本地日期、
// 指定时区墙钟。组件里不再出现 `new Date(iso)` / `Date.parse(iso)` / 字符串裁剪——
// 那三种写法会各自解释时区与格式，最后得到三个不一样的答案。
//
// 唯一保留的字符串入口是 `toMillis`：口径切换前落库的 WS 事件载荷仍是 RFC3339，
// 事件入口用它归一；新建代码不要再用字符串时间。

/**
 * 宽容归一：数字毫秒 / RFC3339 字符串 / 十进制字符串 → Unix 毫秒；空/非法 → `undefined`。
 *
 * 只给**历史事件载荷**与 automation 的 `schedule.at`（定义载体，仍是 RFC3339）用。
 * 正常接口响应已经是数字，调用方不需要这层。
 */
export function toMillis(value: unknown): UnixMillis | undefined {
  if (typeof value === 'number') {
    return Number.isFinite(value) && value > 0 ? value : undefined
  }
  if (typeof value === 'string' && value !== '') {
    if (/^\d+$/.test(value)) {
      const n = Number(value)
      return n > 0 ? n : undefined
    }
    const t = Date.parse(value)
    return Number.isNaN(t) ? undefined : t
  }
  return undefined
}

function dateOf(ms: UnixMillis | undefined): Date | undefined {
  if (!ms || !Number.isFinite(ms)) return undefined
  const d = new Date(ms)
  return Number.isNaN(d.getTime()) ? undefined : d
}

const pad = (n: number): string => String(n).padStart(2, '0')

/**
 * 相对时间标签（列表行用）：刚刚 / N 分钟前 / N 小时前 / 昨天 / N 天前 / MM-DD / YYYY-MM-DD。
 * `now` 可注入（测试稳定）；空/非法时间 → 空串（不显示噪声）。
 */
export function relTime(ms: UnixMillis | undefined, now: number = Date.now()): string {
  const d = dateOf(ms)
  if (!d) return ''
  const diff = now - d.getTime()
  if (diff < 0) return '刚刚' // 时钟偏差（服务端时间略超前）不显示「-1 分钟」
  const min = Math.floor(diff / 60_000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hours = Math.floor(min / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days === 1) return '昨天'
  if (days < 7) return `${days} 天前`
  const sameYear = new Date(now).getFullYear() === d.getFullYear()
  return sameYear ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())}` : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/**
 * 绝对时间标签（浮层用）：**本地时区**，`HH:mm`（当天）/ `MM-DD HH:mm`（同年）/ `YYYY-MM-DD HH:mm`（跨年）。
 */
export function absTime(ms: UnixMillis | undefined, now: number = Date.now()): string {
  const d = dateOf(ms)
  if (!d) return ''
  const n = new Date(now)
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const sameDay = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()
  if (sameDay) return hm
  const sameYear = n.getFullYear() === d.getFullYear()
  const md = `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  return sameYear ? `${md} ${hm}` : `${d.getFullYear()}-${md} ${hm}`
}

/** 精确到秒的本地时间（浮层 tooltip 用；界面上不直接显示秒）。 */
export function fullTime(ms: UnixMillis | undefined): string {
  const d = dateOf(ms)
  if (!d) return ''
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/**
 * 浮层的「更新于」文案：相对时间 + 本地绝对时间（`刚刚 · 18:42` / `3 天前 · 11-02 09:12`）。
 *
 * 相对时间已经退化成日期（≥7 天）时只留绝对时间——否则会出现 `11-02 · 11-02 09:12`
 * 这种同一件事说两遍的文案。空/非法时间 → 空串（该行整行跳过）。
 */
export function updatedLabel(ms: UnixMillis | undefined, now: number = Date.now()): string {
  const rel = relTime(ms, now)
  if (!rel) return ''
  const abs = absTime(ms, now)
  if (!abs) return rel
  const relative = rel === '刚刚' || rel === '昨天' || rel.endsWith('前')
  return relative ? `${rel} · ${abs}` : abs
}

/**
 * 会话行右端的**日期**标签（`MM-DD`；跨年补成 `YYYY-MM-DD`）。
 *
 * 刻意不叫 `relTime`：守卫（`TestSessionRowIsSingleLine` ②）钉住行内不许出现相对时间，
 * 而"3 分钟前"这种相对口径一进来就会让行内文字每秒钟都不一样。日期是稳定的。
 */
export function sessDate(ms: UnixMillis | undefined, now: number = Date.now()): string {
  const d = dateOf(ms)
  if (!d) return ''
  const sameYear = new Date(now).getFullYear() === d.getFullYear()
  return sameYear ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())}` : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** 会话列表分组标签：今天 / 昨天 / M月D日（按本地日历日比较）。 */
export function dayLabel(ms: UnixMillis | undefined, now: number = Date.now()): string {
  const d = dateOf(ms)
  if (!d) return '更早'
  const n = new Date(now)
  const startOfDay = (x: Date): number => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((startOfDay(n) - startOfDay(d)) / 86_400_000)
  if (diff <= 0) return '今天'
  if (diff === 1) return '昨天'
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

/** 本地时间 `YYYY-MM-DD HH:mm`（详情/资料库用）；空/非法 → `—`。 */
export function fmtDateTime(ms: UnixMillis | undefined): string {
  const d = dateOf(ms)
  if (!d) return '—'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 本地时间 `MM-DD HH:mm`（目录/自动化运行史用）；空/非法 → 空串。 */
export function fmtShortDateTime(ms: UnixMillis | undefined): string {
  const d = dateOf(ms)
  if (!d) return ''
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 本地时间 `MM-DD HH:mm:ss`（探测时间用）；空/非法 → `—`。 */
export function fmtProbeAt(ms: UnixMillis | undefined): string {
  const d = dateOf(ms)
  if (!d) return '—'
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/**
 * 指定 IANA 时区的墙钟 `MM-DD HH:mm`（automation 预览/定义展示用）。
 *
 * 入参允许字符串：`schedule.at` 是**用户可编辑的定义载体**（RFC3339），不属于接口时间戳口径；
 * 除此之外都应传毫秒。用 `Intl` 而不是手写偏移——DST 是这里唯一会咬人的东西。
 */
export function fmtLocalInZone(ms: UnixMillis | string, timezone: string): string {
  const at = typeof ms === 'string' ? toMillis(ms) : ms
  const d = dateOf(at)
  if (!d) return '—'
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: timezone,
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }).format(d)
  } catch {
    return '—'
  }
}

/**
 * 本轮已处理时长的展示串（`18.4s`）；不足 1 秒返回空串。
 *
 * **纯函数**且不读时钟：调用方传 `now`，这样它可以每 1s 重渲染一次而保持可测。
 */
export function elapsedLabel(startMs: UnixMillis | undefined, now: number): string {
  if (!startMs) return ''
  const ms = now - startMs
  if (ms < 1000) return ''
  return `${(ms / 1000).toFixed(1)}s`
}
