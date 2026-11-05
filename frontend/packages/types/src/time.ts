/**
 * Unix 毫秒时间戳：**后端 REST/WS 返回的所有"时刻"字段的唯一形态**。
 *
 * 为什么是毫秒：与 `Date.now()` / `new Date(ms)` 同量纲，前端不需要 `*1000`；
 * 为什么是 number：JSON 里就是数字，不需要再解析 ISO 字符串。
 *
 * 例外（不是"时刻"，不要套这个类型）：
 *  - `effective_at` 是枚举（now | next_session | next_launch）；
 *  - `schedule.at` 是用户可编辑的定义（RFC3339，写面仍收 ISO）；
 *  - `usage` 的 from/to 是日历日区间回显；
 *  - `*_ms` 是时长，不是时间点。
 *
 * 历史 WS 载荷兼容：口径切换前落库的事件里 `at`/`timeout_at` 仍是 ISO 字符串，
 * 事件入口用 `toMillis()` 归一（见 `packages/console/src/console/time.ts`）。
 */
export type UnixMillis = number
