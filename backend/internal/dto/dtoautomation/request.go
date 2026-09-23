// Package dtoautomation 承载 automation（定时任务）面的请求/响应 DTO。
//
// 与能力写面同一条纪律：写面请求体是**原文 JSON 对象**（`map[string]json.RawMessage`），
// 因为 `PUT` 的语义是"只改请求体里出现过的键"，键的出现与否是语义而不是编码细节。
package dtoautomation

import "encoding/json"

// AutomationWriteReq 新建/覆盖一条 automation 定义（POST /automations、PUT /automations/:id）。
//
// 能写的键：`id`（仅新建；与路径不一致 → 40000）、`name`、`enabled`、`schedule`、`target`、
// `max_run_duration`、`state`（启用/停用/归档/从 needs_attention 恢复）。
// **不能写**平台独占的运行态键（`last_fired_at`/`last_status`/`last_conversation_id`/
// `last_reason`/`consecutive_failures`/`recent_runs`）→ 40000：`last_fired_at` 就是认领判据，
// 能写它就能让同一次触发重放，或者把一条 automation 永久卡住不跑。
type AutomationWriteReq map[string]json.RawMessage

// AutomationPreviewReq `POST /automations/preview`：只算时间，**不落盘、不建会话**。
//
// 请求体就是一份 automation 定义（写面语义的原文对象），因此"面板里还没保存的schedule"
// 也能先看到它到底什么时候跑——这正是这个端点存在的理由。
type AutomationPreviewReq struct {
	// Schedule 待预览的调度（必填；其余定义字段可省，预览不看 target）。
	Schedule json.RawMessage `json:"schedule"`
	// Count 预览次数（默认 5，上限 20）。
	Count int `json:"count,omitempty"`
	// From 起点（RFC3339；空 = 现在）。给"我想看看那天之后会怎么跑"用。
	From string `json:"from,omitempty"`
}
