// automation（定时任务，M4）对外形态。
//
// 定义与运行态同住一个文件（`<AutomationDir>/<id>.json`），所以这里也是同一个对象：
// 前 6 个字段是**用户写的**（写面收起 `id`/`name`/`enabled`/`schedule`/`target`/`max_run_duration`），
// 其余是**平台写回的运行事实**（写面拒收，见 PUT /automations/:id）。

export type AutomationScheduleKind = 'once' | 'daily' | 'weekly'

/**
 * 调度表达式。三态共用 `timezone`（显式 IANA 名，不猜本地时区），
 * `once` 用 `at`，`daily` 用 `hour`/`minute`，`weekly` 用 `hour`/`minute` + `days_of_week`。
 *
 * `days_of_week` 是 **0=周日** 的整数集合（与 Go `time.Weekday` 对齐）；
 * 服务端容忍重复/乱序并在装载时归一后**回写文件**。
 */
export interface AutomationSchedule {
  kind: AutomationScheduleKind
  timezone: string
  /** kind=once：RFC3339 绝对时刻。 */
  at?: string
  hour?: number
  minute?: number
  days_of_week?: number[]
}

/**
 * 触发时怎么建会话。
 *
 * **没有 runtime_hint**：底座由构建期固定（`-X pkg/runtime.BuildRuntime`）。老定义里若还留着
 * `target.runtime_hint`，写面会被服务端 40001 拒绝（`runtime_selection_removed`），不会静默忽略。
 */
export interface AutomationTarget {
  agent_id: string
  /** 工作空间（**创建时钉住**；必须非空绝对路径）。 */
  cwd: string
  model?: string
  prompt: string
}

/** `needs_attention` = 最近一次没跑成（连续失败计数 > 0），需要人看一眼。 */
export type AutomationState = 'active' | 'inactive' | 'archived' | 'needs_attention'

/** 一次触发的结论：claimed（建了会话）/ duplicate（这次已触发过）/ overlap（上一次还在跑）/ skipped（触发失败）。 */
export type AutomationRunStatus = 'claimed' | 'duplicate' | 'overlap' | 'skipped'

export type AutomationRunTrigger = 'scheduled' | 'recovery' | 'manual'

/** 运行史的一条（最新在前，最多 20 条；**含没跑成的那些**）。 */
export interface AutomationRunRecord {
  scheduled_for: string
  fired_at?: string
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
  next_due_at?: string
  last_fired_at?: string
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

/** 预览的一项：`at` 给机器，`wall` + `timezone` 给人（"我设的 9 点到底几点跑"）。 */
export interface AutomationPreviewItem {
  at: string
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
