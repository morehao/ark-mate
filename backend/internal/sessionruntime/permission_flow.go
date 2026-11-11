package sessionruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/object/objtime"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

// requestPermission 处理一次 ACP 权限请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 会话级始终允许记忆命中 → 直接放行（快速路径）；
//   - 否则发布 session.permission_request 事件（含 call_id/options）并阻塞等待
//     PermissionsResolve（allow/reject × once/always）；超时或 ctx 取消自动拒绝。
//
// 事件发布（publish）注入的给 interaction.PermissionManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestPermission(ctx context.Context, req protocol.PermissionRequest) protocol.PermissionDecision {
	// P3 双 id：权限请求只携带原生 session id → 解析为平台会话 uuid 键控（记忆/挂起/事件
	// 均以平台会话 id 为键，与 sessions/events/permission_memories 一致）。
	sid := o.handles.ResolveProvider(req.SessionID)
	if sid == "" {
		sid = req.SessionID // 兜底回落原生（理论上不应出现；保留事件不丢）
	}
	// v19 起没有 per-agent 默认权限策略这一层（agent 面删除）：裁决只剩**会话级记忆**
	// （用户"始终允许"过的）与会话级征询。`session.permission_policy` 事件常量仍冻结在
	// events.go（对外契约），但已经没有生产者。
	if d, ok := o.perm.AutoDecision(sid, req); ok {
		return d
	}
	publish := func(callID, title string, tool protocol.ToolCallInfo, opts []protocol.PermissionOption, timeout time.Time) {
		// 「等用户决策」是**插曲不是终态**（S1）：它让界面说得出「等待你确认授权」，
		// 而不是拿「进行中」蒙混过去。进入等待前的状态由域里的投影记着，决策后由
		// `ResumeRunStatus` 送回去（否则头栏会一直停在等待态）。
		o.runner.RunStatusFor(sid, session.RunStatusWaitingForPermission, session.RunStatusOpts{
			ToolCallID: tool.ID, ToolName: title, WaitingFor: "permission",
		})
		o.svc.sessionPlane.Publish(session.BusPermissionRequest, sid, map[string]any{
			"session_id":   sid,
			"call_id":      callID,
			"title":        title,
			"tool_call_id": tool.ID,
			"kind":         tool.Kind,
			"input":        tool.Input,
			"options":      opts,
			"timeout_at":   objtime.Milli(timeout), // Unix 毫秒；UI 端超时收敛（无用户决策时自动拒绝）
		})
	}
	return o.perm.RegisterAndWait(ctx, sid, req, publish)
}

// PendingPermissions 列出某会话的挂起权限请求（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingPermissions(sessionID string) []interaction.PermissionItem {
	return o.perm.PendingItems(sessionID)
}

// ResolvePermission 按 call_id 投递决策（allow/reject × once/always），并返回**实际回执**
// （ACP 形态 selected/cancelled + 有效 option_id；规则见 protocol.EffectivePermissionOutcome）。
// 拒绝分支不再折叠：optionID（所选 deny 选项）与 always 标记透传给 interaction.PermissionManager，
// ACP 层对显式拒绝回 Selected{deny 选项}，agent 可区分「拒绝」与「取消/超时」。
//
// sessionID 为决策发起方会话（C3 scope 归属）：与挂起项归属不符时按「不存在」处理
// （ok=false，不投递、不删除挂起项，不泄露他会话挂起项）——与 elicitation 决策同一立场。
func (o *Orchestrator) ResolvePermission(sessionID, callID, optionID string, always, allowed bool) (PermissionDecisionOutcome, bool, error) {
	if callID == "" {
		return PermissionDecisionOutcome{}, false, failure.NewError(failure.ClassRequest, "call_id_required", "call_id 必填", nil)
	}
	d := protocol.PermissionDecision{Allowed: allowed, Rejected: !allowed, OptionID: optionID, Always: always}
	found, out := o.perm.ResolveDecision(sessionID, callID, d)
	if !found {
		return PermissionDecisionOutcome{}, false, nil
	}
	// 等待结束：把**进入等待前**那一次状态送回去（等待是插曲，不是终态）。不发这一条的后果
	// 不是报错，而是头栏**永久**停在「等待你确认授权」——卡片已消、那一轮已经在跑。
	o.svc.sessionPlane.ResumeRunStatus(sessionID)
	return PermissionDecisionOutcome{
		Allowed:  d.Allowed,
		Rejected: d.Rejected,
		Always:   d.Always,
		OptionID: optionID,
		Kind:     out.Kind,
		Selected: out.OptionID,
	}, true, nil
}

// PermissionDecisionOutcome 是权限决策的对外回执（REST 响应字段来源；不含任何请求参数回显猜测）。
type PermissionDecisionOutcome struct {
	Allowed  bool   // 决策是否放行
	Rejected bool   // 决策是否显式拒绝
	Always   bool   // 是否要求写入会话级记忆
	OptionID string // 请求里指定的 option_id（原样回带，便于前端核对）
	Kind     string // 实际 ACP 回执形态：selected | cancelled
	Selected string // 实际选中的 option_id（Kind=cancelled 时为空）
}

// PermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧；供详情/撤销 UI）。
func (o *Orchestrator) PermissionMemories(sessionID string) []interaction.MemoryItem {
	return o.perm.List(sessionID)
}

// RevokeMemory 撤销会话级权限记忆。
//   - title、kind 均非空 → 撤销单条（精确匹配 action+identifier）；
//   - 两者均为空 → 一键撤销整会话全部记忆（= ForgetSession）。
func (o *Orchestrator) RevokeMemory(sessionID, title, kind string) (bool, error) {
	switch {
	case title == "" && kind == "":
		o.perm.ForgetSession(sessionID)
		return true, nil
	case title == "" || kind == "":
		return false, failure.NewError(failure.ClassRequest, "memory_revoke_params", "撤销单条需 title 与 kind 同时提供；两者都空则撤销全部", nil)
	}
	return o.perm.Revoke(sessionID, title, kind), nil
}

// resolveElicitationScope 判定一次 elicitation 的会话归属（C3），返回 (会话 id, 归属来源)：
//   - wire：acp 层已从 `_meta` 读到会话（ScopeWire）→ 直接采用，不做推断；
//   - inferred_inflight：`_meta` 未给，但连接上恰有一个在途 run → 按它归属；
//   - ambiguous：两者都不成立（空闲 / 会话外 request-scope / 多会话并发）→ 空 id，调用方按取消
//     回应并留痕，不猜测（猜错会把提问卡投到别的会话上，比不投更坏）。
func (o *Orchestrator) resolveElicitationScope(cli protocol.Conn, req protocol.ElicitationRequest) (sessionID, scopeSource, ambiguousNote string) {
	// D-4（v15 顺手修的既有缺陷）：`req.SessionID` 来自 ACP `_meta`，那是**底座的会话 id**，
	// 不是平台的会话 id。v15 之前两者同形（有些底座就发 `ses_…`），于是"把提问卡挂到一个
	// 不存在的会话上"在任何测试与人工核对里都看不出来；加了 `rt_` 壳之后形状不同了，**不查就错**。
	//
	// 修法只有一条：先按 `runtime_session_id` 反查平台会话，查不到退 `ScopeAmbiguous` + 留痕，
	// **不猜**（这个函数的既有口径就是"猜错比不投更坏"）。
	if req.ScopeSource == protocol.ScopeWire && req.SessionID != "" {
		if sid := o.platformSessionIDForProvider(req.SessionID); sid != "" {
			return sid, protocol.ScopeWire, ""
		}
		return "", protocol.ScopeAmbiguous,
			"elicitation 的 wire `_meta` 带来了一个底座会话 id，但平台上没有会话登记它" +
				"（跨会话 / 已删除 / 底座换了 id）：按不可归属处理，不猜测"
	}
	if sid := o.busySessionForClient(cli); sid != "" {
		return sid, protocol.ScopeInferred, ""
	}
	return "", protocol.ScopeAmbiguous,
		"elicitation 无法归属会话（wire `_meta` 未给 sessionId，且连接上无唯一在途 run）"
}

// platformSessionIDForProvider 把**底座会话原文**反查成平台会话 id（D-4 的唯一实现）。
//
// 两级：① 进程内的 `byProvider`（活跃会话，与 ACP 回调同源）；② 持久层（进程重启后
// `_meta` 里的 id 可能指向一条历史会话）。查不到返回空串——调用方退 `ScopeAmbiguous`，不猜。
func (o *Orchestrator) platformSessionIDForProvider(rawProviderID string) string {
	if rawProviderID == "" {
		return ""
	}
	if sid := o.handles.ResolveProvider(rawProviderID); sid != "" {
		return sid
	}
	wrapped, err := objid.WrapRuntime(rawProviderID)
	if err != nil {
		return ""
	}
	if sid, ok := o.svc.sessionPlane.ByRuntimeSessionID(wrapped); ok {
		return sid
	}
	st := o.svc.planes.Resources.Store()
	if st == nil {
		return ""
	}
	list, err := st.ListSessions(context.Background(), nil)
	if err != nil {
		return ""
	}
	for i := range list {
		if list[i].RuntimeSessionID == wrapped {
			return list[i].ID
		}
	}
	return ""
}

// requestElicitation 处理一次 ACP 结构化提问请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 发布 session.elicitation_request 事件（含 elicitation_id/mode/message/schema|url/timeout_at）
//     并阻塞等待 ElicitationResolve（accept/decline/cancel）；超时或 ctx 取消自动按取消处理。
//
// 事件发布（publish）注入给 interaction.ElicitationManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestElicitation(ctx context.Context, req protocol.ElicitationRequest) protocol.ElicitationOutcome {
	sid := req.SessionID // 断点恢复别名归一
	publish := func(it interaction.ElicitationItem) {
		o.runner.RunStatusFor(sid, session.RunStatusWaitingForUser, session.RunStatusOpts{WaitingFor: "elicitation"})
		payload := map[string]any{
			"session_id":     sid,
			"scope_source":   it.ScopeSource,
			"elicitation_id": it.ID,
			"mode":           it.Mode,
			"message":        it.Message,
			"timeout_at":     objtime.Milli(it.TimeoutAt),
		}
		if it.Mode == "form" {
			payload["schema"] = it.Schema
		} else {
			payload["url"] = it.URL
		}
		o.svc.sessionPlane.Publish(session.BusElicitationRequest, sid, payload)
	}
	return o.elic.RegisterAndWait(ctx, sid, req, publish)
}

// PendingElicitations 列出某会话的挂起结构化提问（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingElicitations(sessionID string) []interaction.ElicitationItem {
	return o.elic.PendingItems(sessionID)
}

// AskElicitation 处理一次**外部 source**（answerer 桥接 / 非 ACP 底座）发起、需阻塞等待用户
// 决策的结构化提问：转成内部 ElicitationRequest 后走与 ACP elicitation **同一套**挂起/事件/决策链。
//
// 与 `requestElicitation`（ACP elicitation/create 的入口）的分界：那条的请求来自底座读循环，
// 归属要 `resolveElicitationScope` 推断（D1）；本条的 `sessionID` 由**调用方显式给出**（answerer
// 插件从 tool-call 上下文拿到），因此 scope_source 直接是 `wire`——不推断、不歧义。
//
// 这正是设计文档 `2026-11-11-ask-user-answerer-bridge-design.md` §8.4 的落点：answerer 路径
// 绕开 D1。返回值是内部 ElicitationOutcome（accept/decline/cancel + content），由适配器翻译成
// 对外 DTO。超时/取消语义完全复用 `RegisterAndWait`（ADR-11：超时转 pending，卡片保留可答）。
//
// 空 sessionID 直接拒绝：外部提问必须有确定归属，否则宁可失败也不要挂到错误会话（与 D1 的
// 「不猜测」同一立场）。
func (o *Orchestrator) AskElicitation(ctx context.Context, sessionID string, req protocol.ElicitationRequest) (protocol.ElicitationOutcome, error) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return protocol.ElicitationOutcome{}, failure.NewError(failure.ClassRequest,
			"elicitation_scope_required", "外部结构化提问必须指定归属会话", nil)
	}
	// 显式归属：ScopeWire（调用方从 tool-call 上下文给出，无需推断）。
	req.SessionID = sid
	req.ScopeSource = protocol.ScopeWire
	publish := func(it interaction.ElicitationItem) {
		o.runner.RunStatusFor(sid, session.RunStatusWaitingForUser, session.RunStatusOpts{WaitingFor: "elicitation"})
		payload := map[string]any{
			"session_id":     sid,
			"scope_source":   it.ScopeSource,
			"elicitation_id": it.ID,
			"mode":           it.Mode,
			"message":        it.Message,
			"timeout_at":     objtime.Milli(it.TimeoutAt),
		}
		if it.Mode == "form" {
			payload["schema"] = it.Schema
		} else {
			payload["url"] = it.URL
		}
		o.svc.sessionPlane.Publish(session.BusElicitationRequest, sid, payload)
	}
	return o.elic.RegisterAndWait(ctx, sid, req, publish), nil
}

// ResolveElicitation 按 elicitation_id 投递决策（accept/decline/cancel；accept 可带 content）。
// sessionID 是决策发起方会话：C3 起做 scope 校验，只有归属会话能决策（跨会话 → not found）。
// `late=true` 走另一条路（ADR-11）：那条提问已经超时转 pending，那一轮的 ACP 调用早已拿到
// `cancel` 返回，所以回答**不能**塞回旧轮——它作为**新一轮**的用户消息进去（见 `lateElicitation`）。
func (o *Orchestrator) ResolveElicitation(sessionID, elicitationID, action string, content map[string]any, late bool) (bool, error) {
	if elicitationID == "" {
		return false, failure.NewError(failure.ClassRequest, "elicitation_id_required", "elicitation_id 必填", nil)
	}
	if late {
		return o.lateElicitation(sessionID, elicitationID, action, content)
	}
	var found, mismatch bool
	switch action {
	case "accept":
		found, mismatch = o.elic.ResolveAccept(sessionID, elicitationID, content)
	case "decline":
		found, mismatch = o.elic.ResolveDecline(sessionID, elicitationID)
	case "cancel":
		found, mismatch = o.elic.ResolveCancel(sessionID, elicitationID)
	default:
		return false, failure.NewError(failure.ClassRequest, "elicitation_action", "action 需为 accept/decline/cancel 之一", nil)
	}
	if mismatch {
		// 归属不符：对外与「不存在」同表现（不泄露其它会话的挂起项），细节已在 manager 侧告警。
		return false, failure.NewError(failure.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil)
	}
	// 等待结束（同权限：等待是插曲，决策后要把进入等待前的阶段送回去）。
	if found {
		o.svc.sessionPlane.ResumeRunStatus(sessionID)
	}
	return found, nil
}

// CancelElicitations 归档时作废该会话的挂起提问（`reason:archive`）。
func (o *Orchestrator) CancelElicitations(sessionID string) {
	o.elic.CancelSessionReason(sessionID, session.ElicitationReasonArchive)
}

// lateElicitation 回答一条**已经超时转 pending** 的提问（ADR-11）。
//
// 与常态那一支的分界不是"参数不同"，而是**语义不同**：
//
//	常态（还在阻塞等）  回答投给那一轮 → 底座的 elicitation/create 拿到结果 → 那一轮继续
//	迟到（已超时转 pending） 那一轮早已收尾 → 回答**作为新一轮**的用户消息进去
//
// `accept` 才会起新一轮；`decline`/`cancel` 只是把卡片收敛（用户不想答，没有内容可发）。
//
// 起新一轮用 `o.Prompt`（队列/忙判据/开轮三连全都在那一条路上）——不直接调底座：那会绕开
// 忙碌判定与事件铸造，而"绕过的那一半"在界面上完全看不出来。
func (o *Orchestrator) lateElicitation(sessionID, elicitationID, action string, content map[string]any) (bool, error) {
	message, found, mismatch := o.elic.ResolveLate(sessionID, elicitationID)
	if mismatch {
		return false, failure.NewError(failure.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil)
	}
	if !found {
		// 不是 pending 态：说明它还在等（走常态）或压根不存在。两种都**不**猜——交给调用方
		// 按 `late=false` 重试，或者如实回"不存在"（与常态那一支同一条出路）。
		return false, failure.NewError(failure.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不在「超时仍可回答」状态", elicitationID), nil)
	}
	// 卡片收敛：`reason:late` 说得出"这是迟到的回答"（与 `user` 分开，界面文案不同）。
	o.svc.sessionPlane.Publish(session.BusElicitationResolved, sessionID, map[string]any{
		"session_id":     sessionID,
		"elicitation_id": elicitationID,
		"action":         action,
		"reason":         "late",
	})
	if action != "accept" {
		return true, nil
	}
	text := lateAnswerText(message, content)
	if text == "" {
		return true, nil
	}
	if _, err := o.Prompt(context.Background(), sessionID, text); err != nil {
		return true, err
	}
	return true, nil
}

// lateAnswerText 把迟到回答拼成一条**用户消息**的正文（确定性顺序：按 key 排序）。
//
// 为什么不发明一个"结构化回答"的消息类型：那一轮已经结束了，这条东西在历史里就是**用户说的话**
// ——界面上它必须与其它用户消息长得一样，否则下一轮读它的模型会看到一个没人定义过的形状。
func lateAnswerText(question string, content map[string]any) string {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	if question != "" {
		b.WriteString("关于「")
		b.WriteString(question)
		b.WriteString("」的回答：")
	} else {
		b.WriteString("关于上一个提问的回答：")
	}
	if len(keys) == 0 {
		b.WriteString("（无内容）")
		return b.String()
	}
	for _, k := range keys {
		b.WriteString("\n- ")
		b.WriteString(k)
		b.WriteString("：")
		b.WriteString(fmt.Sprintf("%v", content[k]))
	}
	return b.String()
}
