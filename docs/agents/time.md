# 时间字段口径：后端返回 Unix 毫秒，前端按需格式化

> 从仓库根 `AGENTS.md` 拆出（2026-11-05）。本文件是**唯一事实源**；`AGENTS.md` 只留索引与硬约束。

## 口径

- **REST 响应与 WS 事件载荷里的"瞬时"一律 Unix 毫秒 `int64`**（`created_at` / `updated_at` / `started_at` / `ended_at` / `timeout_at` / `next_due_at` / `last_fired_at` / `probed_at` / `checked_at` / `since` / `at` / `ts` …）。
- **前端类型是 `UnixMillis = number`**（`packages/types/src/time.ts`），只在展示层转成相对时间/本地日期/指定时区墙钟。
- **零值语义不变**：`0` = 未发生，配 `omitempty`；不引入 `null`。Go 侧必须走 `internal/core/object/objtime` 的 `Milli` / `Millis`，不要直接 `UnixMilli()`——`time.Time{}` 会变成年份 1 的巨大负数。
- **请求参数不套这个口径**：查询/写面继续收 RFC3339 或 `YYYY-MM-DD`（`usage` 的 from/to、`automations/preview` 的 from、`schedule.at` 等）。它们是**人写的输入**，不是服务端产生的时间事实。

## 破坏性

这是一次**破坏性 wire 变更**：任何按字符串解析 `created_at` 的外部脚本都会失效。前端与核心同仓、构建期绑定，所以本次没有改 `version.APIContract`；发布时按团队流程决定是否升 `v2 → v3` 并在发布说明里标注。历史事件载荷不回写，读侧兼容见下节。

## 为什么

- 与 JS `Date.now()` / `new Date(ms)` 同量纲，前端读侧零换算；秒级每处 `*1000` 迟早漏一处。
- 毫秒保留亚秒，事件排序/轮次计时不丢精度；RFC3339 字符串的时区写法（`Z` 与 `+08:00`）不能直接字符串比较。
- 一个字段一种类型，避免同一个响应里 `created_at` 是数字、`recent_runs[].scheduled_for` 是字符串。

## 例外（明确不转）

| 位置 | 保持什么 | 理由 |
| --- | --- | --- |
| `effective_at` | 枚举 `now \| next_session \| next_launch` | 它不是时刻 |
| `first_token_ms` / `duration_ms` | 数字时长 | 不是时间点 |
| `GET /usage/summary` 的 `from` / `to` | `YYYY-MM-DD` / RFC3339 | 日历日区间回显；转毫秒会改变"用户本地哪一天"的语义 |
| `automation.schedule.at` | RFC3339 | **用户可编辑的定义载体**，写面仍收 ISO；GET→PUT 回灌不能被破坏 |
| transcript `.jsonl` 的 `ts`/`created_at` | RFC3339Nano | 文件载体（可回读、可导入） |
| 批量导出的正文、导入导出文件 | RFC3339 | 同上 |
| SQLite 时间列 | RFC3339Nano 文本 | 落库硬契约（`internal/persistence/model` 的 serializer 钉着） |
| `events.payload_json` 里的**历史** `at`/`timeout_at` | 旧值原样（ISO） | 事件日志不可变，不回写；读侧宽容 |

## 读侧兼容（历史事件）

口径切换之前落库的 `run.status.at` / `permission_request.timeout_at` 仍是 RFC3339 字符串。事件不能回写，所以：

- 后端读路径用 `objtime.Parse` / `RunStatusSnapshot.Millis` 同时认字符串与数字；
- 前端事件入口用 `packages/console/src/console/time.ts` 的 `toMillis()` 归一；组件里的格式化函数只收 `number`。

新建代码**只写毫秒**；`toMillis` 只留给历史载荷与 `schedule.at` 这类定义载体。

## 实现位置

- 后端出口工具：`internal/core/object/objtime`（`Milli` / `Millis` / `Parse`）。
- 对外类型：`internal/dto/*` 的字段直接是 `int64`；domain view 与 DTO 同形。
- 事件载荷：`domain/session/runstatus.go`、`sessionruntime/permission_flow.go`、`domain/automation/sched.go`。
- 前端类型：`packages/types/src/time.ts` 的 `UnixMillis`。
- 前端格式化：`packages/console/src/console/time.ts`（`relTime` / `absTime` / `fullTime` / `updatedLabel` / `sessDate` / `dayLabel` / `fmtDateTime` / `fmtShortDateTime` / `fmtProbeAt` / `fmtLocalInZone` / `elapsedLabel`）。
- 定义文件投影：`internal/dto/dtoautomation` 的 `RunRecordResp`（文件里的 `time.Time` 不直接进响应）。

## 新增字段 checklist

1. 先问：它是"服务端产生的时刻"还是"用户输入/文件载体/枚举/时长"？只有第一种进毫秒口径。
2. Go 字段写 `int64`（可空用 `*int64` 或 `omitempty` + 0），投影用 `objtime.Milli`。
3. 前端类型写 `UnixMillis`，展示统一走 `console/time.ts`；**不要**在组件里 `new Date(iso)` / `Date.parse` / 字符串裁剪。
4. 若它是 durable 事件载荷，读路径要能接住旧 ISO。

## 守卫

`backend/internal/archtest/wire_time_test.go` 的 `TestDTOInstantFieldsAreUnixMillis`：扫 `internal/dto/*/*.go`，字段名以 `At`/`Since` 结尾或叫 `TS`/`Ts` 的必须是 `int64`/`*int64`；非时间的同名键（目前只有 `EffectiveAt`）逐条登记并双向检查。新增/删除登记后按实际值同步 [`guards.md`](guards.md) 的计数与 [`guards-history.md`](guards-history.md)。
