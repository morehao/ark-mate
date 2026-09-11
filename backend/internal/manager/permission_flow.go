package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
)

// requestPermission 处理一次 ACP 权限请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 会话级始终允许记忆命中 → 直接放行（快速路径）；
//   - 否则发布 conversation.permission_request 事件（含 call_id/options）并阻塞等待
//     PermissionsResolve（allow/reject × once/always）；超时或 ctx 取消自动拒绝。
//
// 事件发布（publish）注入的给 PermissionManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestPermission(ctx context.Context, req acl.PermissionRequest) acl.PermissionDecision {
	// P3 双 id：权限请求只携带原生 session id → 解析为平台会话 uuid 键控（记忆/挂起/事件
	// 均以平台会话 id 为键，与 conversations/events/permission_memories 一致）。
	sid := o.resolveProvider(req.SessionID)
	if sid == "" {
		sid = req.SessionID // 兜底回落原生（理论上不应出现；保留事件不丢）
	}
	// M2 #7 per-agent 默认权限策略：运维护栏优先于会话级记忆（策略 allow/deny 直接决策，
	// 不经用户；审计事件 session.permission_policy 留痕）。ask/未声明 → 回落既有路径。
	if action, agentID, ok := o.policyAction(sid, req); ok {
		if d, dok := autoPermissionDecision(action, req.Options); dok {
			o.svc.publish(BusPermissionPolicy, sid, map[string]any{
				"session_id": sid, "agent_id": agentID, "action": action,
				"title": req.ToolCall.Title, "kind": req.ToolCall.Kind,
				"allowed": d.Allowed, "rejected": d.Rejected, "option_id": d.OptionID,
			})
			o.logger.Info("permission: auto-decided by agent policy", "conversation", sid,
				"agent", agentID, "action", action, "title", req.ToolCall.Title, "kind", req.ToolCall.Kind)
			return d
		}
		o.logger.Warn("permission: agent policy action has no matching option, fallback to ask",
			"conversation", sid, "agent", agentID, "action", action, "options", len(req.Options))
	}
	if d, ok := o.perm.AutoDecision(sid, req); ok {
		return d
	}
	publish := func(callID, title string, tool acl.ToolCallInfo, opts []acl.PermissionOption, timeout time.Time) {
		o.svc.publish(BusPermissionRequest, sid, map[string]any{
			"session_id":   sid,
			"call_id":      callID,
			"title":        title,
			"tool_call_id": tool.ID,
			"kind":         tool.Kind,
			"input":        tool.Input,
			"options":      opts,
			"timeout_at":   timeout.UTC().Format(time.RFC3339), // UI 端超时收敛（无用户决策时自动拒绝）
		})
	}
	return o.perm.RegisterAndWait(ctx, sid, req, publish)
}

// PendingPermissions 列出某会话的挂起权限请求（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingPermissions(conversationID string) []PermissionItem {
	ps := o.perm.pendingForConversation(conversationID)
	out := make([]PermissionItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, PermissionItem{
			CallID:    p.callID,
			SessionID: p.conversationID,
			Title:     p.title,
			ToolCall:  p.toolCall,
			Options:   p.options,
			TimeoutAt: p.timeout,
		})
	}
	return out
}

// ResolvePermission 按 call_id 投递决策（allow/reject × once/always），并返回**实际回执**
// （ACP 形态 selected/cancelled + 有效 option_id；规则见 acl.EffectivePermissionOutcome）。
// 拒绝分支不再折叠：optionID（所选 deny 选项）与 always 标记透传给 PermissionManager，
// ACP 层对显式拒绝回 Selected{deny 选项}，agent 可区分「拒绝」与「取消/超时」。
func (o *Orchestrator) ResolvePermission(callID, optionID string, always, allowed bool) (PermissionDecisionOutcome, bool, error) {
	if callID == "" {
		return PermissionDecisionOutcome{}, false, acl.NewError(acl.ClassRequest, "call_id_required", "call_id 必填", nil)
	}
	d := acl.PermissionDecision{Allowed: allowed, Rejected: !allowed, OptionID: optionID, Always: always}
	found, out := o.perm.ResolveDecision(callID, d)
	if !found {
		return PermissionDecisionOutcome{}, false, nil
	}
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
func (o *Orchestrator) PermissionMemories(conversationID string) []MemoryItem {
	return o.perm.List(conversationID)
}

// RevokeMemory 撤销会话级权限记忆。
//   - title、kind 均非空 → 撤销单条（精确匹配 action+identifier）；
//   - 两者均为空 → 一键撤销整会话全部记忆（= ForgetConversation）。
func (o *Orchestrator) RevokeMemory(conversationID, title, kind string) (bool, error) {
	switch {
	case title == "" && kind == "":
		o.perm.ForgetConversation(conversationID)
		return true, nil
	case title == "" || kind == "":
		return false, acl.NewError(acl.ClassRequest, "memory_revoke_params", "撤销单条需 title 与 kind 同时提供；两者都空则撤销全部", nil)
	}
	return o.perm.Revoke(conversationID, title, kind), nil
}

// requestElicitation 处理一次 ACP 结构化提问请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 发布 conversation.elicitation_request 事件（含 elicitation_id/mode/message/schema|url/timeout_at）
//     并阻塞等待 ElicitationResolve（accept/decline/cancel）；超时或 ctx 取消自动按取消处理。
//
// 事件发布（publish）注入给 ElicitationManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestElicitation(ctx context.Context, req acl.ElicitationRequest) acl.ElicitationOutcome {
	sid := req.SessionID // 断点恢复别名归一
	publish := func(it ElicitationItem) {
		payload := map[string]any{
			"session_id":      sid,
			"conversation_id": sid,
			"scope_source":    it.ScopeSource,
			"elicitation_id":  it.ID,
			"mode":            it.Mode,
			"message":         it.Message,
			"timeout_at":      it.TimeoutAt.UTC().Format(time.RFC3339),
		}
		if it.Mode == "form" {
			payload["schema"] = it.Schema
		} else {
			payload["url"] = it.URL
		}
		o.svc.publish(BusElicitationRequest, sid, payload)
	}
	return o.elic.RegisterAndWait(ctx, sid, req, publish)
}

// PendingElicitations 列出某会话的挂起结构化提问（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingElicitations(conversationID string) []ElicitationItem {
	ps := o.elic.pendingForConversation(conversationID)
	out := make([]ElicitationItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, ElicitationItem{
			ID:             p.id,
			SessionID:      p.conversationID,
			ConversationID: p.conversationID,
			ScopeSource:    p.scopeSource,
			Mode:           p.mode,
			Message:        p.message,
			Schema:         p.schema,
			URL:            p.url,
			TimeoutAt:      p.timeout,
		})
	}
	return out
}

// ResolveElicitation 按 elicitation_id 投递决策（accept/decline/cancel；accept 可带 content）。
// conversationID 是决策发起方会话：C3 起做 scope 校验，只有归属会话能决策（跨会话 → not found）。
func (o *Orchestrator) ResolveElicitation(conversationID, elicitationID, action string, content map[string]any) (bool, error) {
	if elicitationID == "" {
		return false, acl.NewError(acl.ClassRequest, "elicitation_id_required", "elicitation_id 必填", nil)
	}
	var found, mismatch bool
	switch action {
	case "accept":
		found, mismatch = o.elic.ResolveAccept(conversationID, elicitationID, content)
	case "decline":
		found, mismatch = o.elic.ResolveDecline(conversationID, elicitationID)
	case "cancel":
		found, mismatch = o.elic.ResolveCancel(conversationID, elicitationID)
	default:
		return false, acl.NewError(acl.ClassRequest, "elicitation_action", "action 需为 accept/decline/cancel 之一", nil)
	}
	if mismatch {
		// 归属不符：对外与「不存在」同表现（不泄露其它会话的挂起项），细节已在 manager 侧告警。
		return false, acl.NewError(acl.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil)
	}
	return found, nil
}
