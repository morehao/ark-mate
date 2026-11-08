import type { UnixMillis } from './time'

// automation（定时任务，M4）对外形态。
//
// 定义与运行态同住一个文件（`<AutomationDir>/<id>.json`），所以这里也是同一个对象：
// 前 6 个字段是**用户写的**（写面收起 `id`/`name`/`enabled`/`schedule`/`target`/`max_run_duration`），
// 其余是**平台写回的运行事实**（写面拒收，见 PUT /automations/:id）。

export type AutomationScheduleKind = 'once' | 'interval' | 'daily' | 'weekly' | 'monthly' | 'rrule'

/**
 * 调度表达式。共用 `timezone`（显式 IANA 名，不猜本地时区），
 * `once` 用 `at`，`interval` 用 `every_seconds`，`daily` 用 `hour`/`minute`，
 * `weekly` 用 `hour`/`minute` + `days_of_week`，`monthly` 用 `hour`/`minute` + `day`，
 * `rrule` 用 `rrule`（两行文本，**时区由 DTSTART 自带、不读 `timezone`**）。
 *
 * **没有 cron**（ADR v35/v36）：rrule 是 RFC 5545 受限子集，比 cron 方言更不易写歪。
 *
 * `days_of_week` 是 **0=周日** 的整数集合（与 Go `time.Weekday` 对齐）；
 * 服务端容忍重复/乱序并在装载时归一后**回写文件**。
 * `every_seconds` ≥ 60（interval，纯加时长、与 DST 无关）。
 * `day` 1–31（monthly，当月无此日则跳过）。
 * `rrule` 形如 `DTSTART:20260101T090000Z\nRRULE:FREQ=DAILY`（权威文本，不规范化）。
 */
export interface AutomationSchedule {
  kind: AutomationScheduleKind
  timezone: string
  /** kind=once：RFC3339 绝对时刻（**定义载体**，写面仍收 ISO；不是接口时间戳口径）。 */
  at?: string
  hour?: number
  minute?: number
  days_of_week?: number[]
  /** kind=interval：每多少秒触发一次（≥60）。 */
  every_seconds?: number
  /** kind=monthly：每月第几日（1–31，当月无此日则跳过）。 */
  day?: number
  /** kind=rrule：两行文本 `DTSTART…\nRRULE:…`（RFC 5545 受限子集；时区取自 DTSTART）。 */
  rrule?: string
}

/**
 * 触发时怎么建会话 / 投递。
 *
 * **投递模式 `delivery`（ADR v35）**：缺省 `session`（投递回既有会话 `session_id`，共享会话历史）；
 * `new_session` 显式备选（新建会话跑，需 `cwd`）。空串在服务端归一到 `session`。
 *
 * **没有 runtime_hint**：底座由构建期固定（`-X pkg/runtime.BuildRuntime`）。老定义里若还留着
 * `target.runtime_hint`，写面会被服务端 40001 拒绝（`runtime_selection_removed`），不会静默忽略。
 */
export type AutomationDelivery = 'session' | 'new_session'

export interface AutomationTarget {
  /**
   * 工作空间（**创建时钉住**；`new_session` 时必填绝对路径）。
   *
   * v19 起没有 `agent_id`：定时任务触发时建的那个会话落在构建期固定的底座上，
   * 没有"由哪个 agent 执行"这个选择面（老定义里若还留着它，写面会被服务端拒绝，不静默忽略）。
   */
  cwd: string
  model?: string
  prompt: string
  /** 投递模式（缺省 session：投递回既有会话）。 */
  delivery?: AutomationDelivery
  /** delivery=session 时必填：目标会话 id。 */
  session_id?: string
}

/** `needs_attention` = 最近一次没跑成（连续失败计数 > 0），需要人看一眼。 */
export type AutomationState = 'active' | 'inactive' | 'archived' | 'needs_attention'

/** 一次触发的结论：claimed（建了会话）/ duplicate（这次已触发过）/ overlap（上一次还在跑）/ skipped（触发失败）。 */
export type AutomationRunStatus = 'claimed' | 'duplicate' | 'overlap' | 'skipped'

export type AutomationRunTrigger = 'scheduled' | 'recovery' | 'manual'

/** 运行史的一条（最新在前，最多 20 条；**含没跑成的那些**）。 */
export interface AutomationRunRecord {
  scheduled_for: UnixMillis
  fired_at?: UnixMillis
  status: AutomationRunStatus
  reason?: string
  /** status=claimed 时非空，可直接跳过去看那一轮。 */
  session_id?: string
  trigger?: AutomationRunTrigger
}

export interface Automation {
  id: string
  name: string
  enabled: boolean
  state: AutomationState
  schedule: AutomationSchedule
  target: AutomationTarget
  max_run_duration?: string
  /** 内存态现算：空 = 没有下一次（跑完的 once / 归档 / 装载失败）。 */
  next_due_at?: UnixMillis
  last_fired_at?: UnixMillis
  last_session_id?: string
  last_status?: AutomationRunStatus
  last_reason?: string
  consecutive_failures?: number
  /** 装载失败的原因（坏定义也出现在列表里，不静默消失）。 */
  error?: string
  recent_runs?: AutomationRunRecord[]
}

/** 写面请求体（原文对象：`null` 删键、未出现的键保留原值）。 */
export type AutomationWriteBody = Record<string, unknown>

export interface AutomationDeleteResp {
  id: string
  deleted: boolean
}

/** 预览的一项：`at` 给机器（Unix 毫秒），`wall` + `timezone` 给人（"我设的 9 点到底几点跑"）。 */
export interface AutomationPreviewItem {
  at: UnixMillis
  wall: string
  timezone: string
  warnings?: string[]
}

export interface AutomationPreviewResp {
  occurrences: AutomationPreviewItem[]
  warnings?: string[]
}

export interface AutomationRunsResp {
  id: string
  runs: AutomationRunRecord[]
  limit: number
}

export interface AutomationRunResp {
  id: string
  status: AutomationRunStatus
  trigger: AutomationRunTrigger
  session_id?: string
  reason?: string
  state?: AutomationState
}
