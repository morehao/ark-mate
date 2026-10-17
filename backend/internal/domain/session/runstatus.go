package session

import (
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
)

// 一轮问答的**过程状态**（ADR-6 / ADR-7）。
//
// 为什么是 durable 事件而不是会话列：`sessions.status` 表达的是**会话的终态归类**
// （in_progress / completed / failed / cancelled / interrupted），粒度太粗——它区分不出
// 「在流式」与「在等授权」；而一次会话有多轮，用列表达过程只能存最后一轮，历史轮的过程全丢。
// 事件天然是每轮的、可回放的、可进 transcript 的。
//
// 取值集**按本仓观测面收窄到 10 态**，不照抄参照产品的 13 态：它的 13 态是它自己 CLI 状态机
// 的投影，而本仓的观测面是 ACP。声明一个观测不到的取值，会让界面在两种状态下显示同一个东西
// （「我设了没反应」的同族问题）。
//
// 本文件是 `run.status` 的**唯一铸造点**：载荷形状、取值校验、去重键都在这里。`manager` 只许
// 调 `Plane.PublishRunStatus`——它连 `BusRunStatus` 这个字面量都不该出现（由 `internal/archtest`
// 的 `TestRunStatusIsSingleSource` 钉住）。
const (
	// RunStatusPreparing 一轮已开始、还没向模型发请求（含排队输入被认领后的启动段）。
	RunStatusPreparing = "preparing"
	// RunStatusModelRequesting 请求已发出、首个增量未到。
	RunStatusModelRequesting = "model_requesting"
	// RunStatusModelStreaming 正在收模型输出（文本/思考增量）。
	RunStatusModelStreaming = "model_streaming"
	// RunStatusToolExecuting 正在执行某个工具（载荷带 tool_name / tool_call_id）。
	RunStatusToolExecuting = "tool_executing"
	// RunStatusWaitingForPermission 等用户决策一条权限请求（**不是终态**：等待是插曲）。
	RunStatusWaitingForPermission = "waiting_for_permission"
	// RunStatusWaitingForUser 等用户回答一条结构化提问（同上，不是终态）。
	RunStatusWaitingForUser = "waiting_for_user"
	// RunStatusRetrying 正在重试上一次失败的调用。
	RunStatusRetrying = "retrying"
	// RunStatusCompacting 正在压缩上下文（**前提**：底座通告压缩，见 ADR-9 与 P1-a 探针）。
	RunStatusCompacting = "compacting"
	// RunStatusCancelling 已受理取消、收尾未完成。
	RunStatusCancelling = "cancelling"
	// RunStatusIdle 本轮收口标记（终态事件紧随其后）。
	//
	// 为什么终态前必须落一次它：界面「停止」的判据必须是**一个状态**，不能是「一段时间没事件」
	// ——后者正是被取代的那套前端推断（`serverBusy`）的做法。
	RunStatusIdle = "idle"
)

// RunStatusInterrupted 恢复路径的**收口**取值（**不在**上面 10 态里）。
//
// 它与 `SessionStatusInterrupted` 刻意同字：两处说的都是「这一轮没有终态事件、进程把它带走了」。
// 它**只能**由恢复路径铸造（`Restore` 的残留 in_progress 收敛）。实时路径永不发它——实时路径
// 知道自己为什么停（主动拆除），发伪终态会污染历史、失败轮列表与重试入口（ADR-8 的前半）。
const RunStatusInterrupted = "interrupted"

// runStatusValues 10 个**活体**过程状态（顺序 = 一轮里的大致发生顺序，供文档与前端联合类型对齐）。
var runStatusValues = []string{
	RunStatusPreparing,
	RunStatusModelRequesting,
	RunStatusModelStreaming,
	RunStatusToolExecuting,
	RunStatusWaitingForPermission,
	RunStatusWaitingForUser,
	RunStatusRetrying,
	RunStatusCompacting,
	RunStatusCancelling,
	RunStatusIdle,
}

// RunStatusValues 返回 10 个活体过程状态（**副本**：调用方改不动本包的取值集）。
func RunStatusValues() []string {
	out := make([]string, len(runStatusValues))
	copy(out, runStatusValues)
	return out
}

// IsLiveRunStatus 判断 v 是否为 10 个活体过程状态之一（恢复收口用的 `interrupted` **不算**）。
func IsLiveRunStatus(v string) bool {
	for _, s := range runStatusValues {
		if s == v {
			return true
		}
	}
	return false
}

// IsRunStatus 判断 v 是否为合法的 `run.status` 取值（= 10 态 ∪ {interrupted}）。
//
// 与 `IsLiveRunStatus` 分开是刻意的：写库/回放校验要接受恢复收口那一支，而**实时**发射点只该
// 产生活体状态（`PublishRunStatus` 用的就是 `IsLiveRunStatus`）。
func IsRunStatus(v string) bool {
	return v == RunStatusInterrupted || IsLiveRunStatus(v)
}

// RunStatusOpts `run.status` 载荷的可选段（零值 = 只发状态）。
type RunStatusOpts struct {
	// Phase 底座给的更细阶段名，**原样透传**（平台不翻译、不重排；缺失时 status 仍可用）。
	Phase string
	// Detail 人话旁白（界面旁白消费；平台自己的判定产物，不是底座字段的复制）。
	Detail string
	// ToolCallID / ToolName 正在执行的工具（`tool_executing` / `waiting_for_permission` 时有值）。
	ToolCallID string
	ToolName   string
	// WaitingFor 在等什么：permission | elicitation | model | tool。
	WaitingFor string
	// Retry 重试信息（attempt, reason）；nil = 不带该键。
	Retry map[string]any
	// At 事件发生时刻；零值取 `time.Now().UTC()`。
	At time.Time
}

// RunStatusPayload 铸造一条 `run.status` 的载荷（**唯一实现**）。
//
// 「唯一」是判据而不是措辞：`internal/archtest` 的 `TestRunStatusIsSingleSource` 要求
// `internal/manager/**` 的生产代码里**零命中** `BusRunStatus` / `"run.status"`——manager 里再手拼
// 一份 `map[string]any` 会让「同状态去重」「取值校验」这两件事悄悄少一半，而两条路径都照常工作。
//
// 载荷里的 `session_id` 与事件的 agg_id 是同一个值：事件载荷一贯带它（前端按载荷渲染，不按帧的
// 元数据），少了它前端要自己从帧里补，而回放出来的历史行不带帧元数据。
func RunStatusPayload(sessionID, runID, status string, opt RunStatusOpts) map[string]any {
	at := opt.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	m := map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"status":     status,
		"at":         at.UTC().Format(time.RFC3339Nano),
	}
	if opt.Phase != "" {
		m["phase"] = opt.Phase
	}
	if opt.Detail != "" {
		m["detail"] = opt.Detail
	}
	if opt.ToolCallID != "" {
		m["tool_call_id"] = opt.ToolCallID
	}
	if opt.ToolName != "" {
		m["tool_name"] = opt.ToolName
	}
	if opt.WaitingFor != "" {
		m["waiting_for"] = opt.WaitingFor
	}
	if opt.Retry != nil {
		m["retry"] = opt.Retry
	}
	return m
}

// RunInterruptedPayload 铸造恢复路径的**收口**载荷（ADR-8 的后半）。
//
// 只带 `reason`（恢复路径的理由恒是 `restart`）：说「失败」是代底座下结论，而平台只知道
// 日志有洞——进程死了，没有别的解释。
func RunInterruptedPayload(sessionID, runID, reason string) map[string]any {
	if reason == "" {
		reason = "restart"
	}
	return map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"status":     RunStatusInterrupted,
		"reason":     reason,
		"at":         time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// PublishRunStatus 发一条**活体**过程状态（去重 + 校验都在这里，manager 只调它）。
//
// 去重（R2）：阶段变化会推帧，一次长工具循环可能推几十帧，`model_streaming` 连发只保留第一帧。
// 键 = `run_id | status | phase | tool_call_id | tool_name | waiting_for`，**不含时刻**：时刻每次都
// 不同，算进键等于不去重。`tool_executing` 因为带 `tool_call_id` 而不会被误合——那是这一支
// 唯一需要连续重复的场景。
//
// 返回值：被去重吞掉时返回 `Seq = -1` 的零事件（与合并窗口的「缓冲中」同一哨兵；v21 起 0 是合法位次，
// 不能拿它当「没发」）。
func (pl *Plane) PublishRunStatus(sessionID, runID, status string, opt RunStatusOpts) eventbus.Event {
	if !IsLiveRunStatus(status) {
		// 未知取值：不发。**不**回落成 idle——那会让「我设了没反应」披上一层正常外观。
		return eventbus.Event{Seq: -1}
	}
	key := runID + "\x00" + status + "\x00" + opt.Phase + "\x00" +
		opt.ToolCallID + "\x00" + opt.ToolName + "\x00" + opt.WaitingFor
	pl.mu.Lock()
	if pl.runStatusKey == nil {
		pl.runStatusKey = make(map[string]string)
	}
	same := pl.runStatusKey[sessionID] == key
	pl.runStatusKey[sessionID] = key
	pl.mu.Unlock()
	if same {
		return eventbus.Event{Seq: -1}
	}
	return pl.Publish(BusRunStatus, sessionID, RunStatusPayload(sessionID, runID, status, opt))
}

// PublishRunInterrupted 发恢复路径的收口（**只许**恢复路径调，见 `RunStatusInterrupted` 注释）。
func (pl *Plane) PublishRunInterrupted(sessionID, runID string) eventbus.Event {
	return pl.Publish(BusRunStatus, sessionID, RunInterruptedPayload(sessionID, runID, "restart"))
}
