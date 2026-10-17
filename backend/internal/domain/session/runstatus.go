package session

import (
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
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

// RunStatusSnapshot 某会话**最近一次** `run.status` 的投影（载荷原样 + 它的位次）。
//
// 为什么投影放在域里而不是"每次读面去查库"：
//   - **一份实现**——`GET /sessions/:id` 的 `run` 段与后续任何读面都读它，不会各查各的；
//   - memory 模式（开发态）没有 events 表，查库那条路直接没有；
//   - 重建只有一处：`Restore` 的**同一次**事件回放顺手把它填上（不再为它多扫一遍库）。
//
// 载荷保留成 map 而不是结构化字段：`run.status` 的载荷是**开放 map**（加键不改 type，R1），
// 投影若定义一套结构体，新增的键就要两处同步——而"两处同步"正是本仓反复吃亏的那类裂缝。
type RunStatusSnapshot struct {
	Payload map[string]any
	Seq     int64
}

// Status 当前状态取值（空串 = 载荷坏了/没有 status 键）。
func (s RunStatusSnapshot) Status() string {
	v, _ := s.Payload["status"].(string)
	return v
}

// Str 取载荷里的一个字符串键（前端/读面装配用的小帮手；缺键返回空串）。
func (s RunStatusSnapshot) Str(key string) string {
	v, _ := s.Payload[key].(string)
	return v
}

// RunSnapshot 取某会话最近一次 `run.status` 的投影（无 → false）。
func (pl *Plane) RunSnapshot(sessionID string) (RunStatusSnapshot, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	s, ok := pl.runStatusLatest[sessionID]
	return s, ok
}

// recordRunStatus 更新投影（**唯一写点**：实时发布、恢复回放、决策恢复三条路都走它）。
//
// 位次单调：回放与实时可能交错（恢复注入历史 + 恢复收口在注入之后），更早的事件不得覆盖更晚的。
func (pl *Plane) recordRunStatus(sessionID string, seq int64, payload map[string]any) {
	if sessionID == "" || seq < 0 || payload == nil {
		return
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.runStatusLatest == nil {
		pl.runStatusLatest = make(map[string]RunStatusSnapshot)
	}
	if prev, ok := pl.runStatusLatest[sessionID]; ok && prev.Seq > seq {
		return
	}
	prev, hadPrev := pl.runStatusLatest[sessionID]
	pl.runStatusLatest[sessionID] = RunStatusSnapshot{Payload: payload, Seq: seq}
	// 「进入等待前是什么状态」：等待是**插曲**，决策后要能回到原来的阶段（否则头栏会一直
	// 停在「等待你确认授权」，而那一轮其实已经继续跑了）。
	if hadPrev {
		if isWaitingStatus(payload["status"]) {
			if pl.runStatusHold == nil {
				pl.runStatusHold = make(map[string]RunStatusSnapshot)
			}
			pl.runStatusHold[sessionID] = prev
		} else {
			delete(pl.runStatusHold, sessionID)
		}
	}
}

// isWaitingStatus 判断某个取值是否属于「在等用户」的那两支（它们是插曲，不是终态）。
func isWaitingStatus(v any) bool {
	s, _ := v.(string)
	return s == RunStatusWaitingForPermission || s == RunStatusWaitingForUser
}

// ResumeRunStatus 退出等待：把**进入等待前**那一次状态再发一遍。
//
// 触发点 = 用户决策落地之后（权限 / 提问）。不发这一条的后果不是报错，而是头栏**永久**停在
// 「等待你确认授权」——卡片已经消掉、那一轮已经在跑，而界面说得像还在等人。
//
// 没有 hold（例如权限被会话级记忆自动放行，压根没进入过等待）→ 返回零事件，静默 no-op。
// 这是刻意的：没有等待就没有「退出等待」这件事，凭空补一条状态等于替运行时宣布阶段。
func (pl *Plane) ResumeRunStatus(sessionID string) eventbus.Event {
	pl.mu.Lock()
	hold, ok := pl.runStatusHold[sessionID]
	pl.mu.Unlock()
	if !ok || hold.Payload == nil {
		return eventbus.Event{Seq: -1}
	}
	status := hold.Status()
	if status == "" || status == RunStatusIdle {
		return eventbus.Event{Seq: -1}
	}
	return pl.PublishRunStatus(sessionID, hold.Str("run_id"), status, RunStatusOpts{
		Phase:      hold.Str("phase"),
		ToolCallID: hold.Str("tool_call_id"),
		ToolName:   hold.Str("tool_name"),
	})
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
	payload := RunStatusPayload(sessionID, runID, status, opt)
	evt := pl.Publish(BusRunStatus, sessionID, payload)
	pl.recordRunStatus(sessionID, evt.Seq, payload)
	return evt
}

// PublishRunInterrupted 发恢复路径的收口（**只许**恢复路径调，见 `RunStatusInterrupted` 注释）。
func (pl *Plane) PublishRunInterrupted(sessionID, runID string) eventbus.Event {
	payload := RunInterruptedPayload(sessionID, runID, "restart")
	evt := pl.Publish(BusRunStatus, sessionID, payload)
	pl.recordRunStatus(sessionID, evt.Seq, payload)
	return evt
}

// RunPhaseOf 把最近一次 `run.status` 的投影折成对外形状（**唯一**一份投影；列表行与详情共用）。
//
// 没有投影（历史会话 / 本轮还没开始）→ nil：JSON 里整个 `run` 键缺席。**不回空壳**——
// 一个 `{status:""}` 会让界面以为「有一个状态，只是空」。
func (pl *Plane) RunPhaseOf(id string) *dtosession.RunPhaseResp {
	snap, ok := pl.RunSnapshot(id)
	if !ok || snap.Status() == "" {
		return nil
	}
	return &dtosession.RunPhaseResp{
		RunID:      snap.Str("run_id"),
		Status:     snap.Status(),
		Phase:      snap.Str("phase"),
		Detail:     snap.Str("detail"),
		ToolCallID: snap.Str("tool_call_id"),
		ToolName:   snap.Str("tool_name"),
		WaitingFor: snap.Str("waiting_for"),
		Reason:     snap.Str("reason"),
		At:         snap.Str("at"),
		Seq:        snap.Seq,
	}
}
