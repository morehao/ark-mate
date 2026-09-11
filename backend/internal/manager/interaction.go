package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/code"
)

// ListPendingPermissions 列出某会话的挂起权限请求。
func (s *Service) ListPendingPermissions(ctx context.Context, id string) ([]dtoconversation.PermissionResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	out := make([]dtoconversation.PermissionResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toPermissionResp(p))
	}
	return out, nil
}

// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回**实际投递给 agent 的回执**
// （allowed/rejected/always/请求 option_id + ACP 形态 outcome/selected_option_id），
// 供 REST 如实回执——不再回显请求参数（option_id 可能被兜底为第一个选项或降级 Cancelled）。
//
// C3 scope 归属：只有登记该请求的会话能决策；跨会话调用（陈旧标签页/串号请求）按「不存在」处理
// （40401，不泄露他会话挂起项）——与 ResolveElicitation 同一立场。
func (s *Service) ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*dtoconversation.PermissionDecisionResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	out, ok, err := s.orch.ResolvePermission(id, callID, optionID, always, allowed)
	if err != nil {
		return nil, code.FromError(err)
	}
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "permission_not_found", fmt.Sprintf("权限请求 %q 不存在或已过期", callID), nil))
	}
	return &dtoconversation.PermissionDecisionResp{
		CallID:           callID,
		Allowed:          out.Allowed,
		Rejected:         out.Rejected,
		Always:           out.Always,
		OptionID:         out.OptionID,
		Outcome:          out.Kind,
		SelectedOptionID: out.Selected,
	}, nil
}

// PendingPermissions 直接透传编排器的挂起权限（供 GetConversation/ListPendingPermissions 复用）。
func (s *Service) PendingPermissions(id string) []PermissionItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingPermissions(id)
}

// PendingElicitations 直接透传编排器的挂起结构化提问（供 ListPendingElicitations 复用）。
func (s *Service) PendingElicitations(id string) []ElicitationItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingElicitations(id)
}

// ListPendingElicitations 列出某会话的挂起结构化提问。
func (s *Service) ListPendingElicitations(ctx context.Context, id string) ([]dtoconversation.ElicitationResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingElicitations(id)
	out := make([]dtoconversation.ElicitationResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toElicitationResp(p))
	}
	return out, nil
}

// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel；accept 可带 content）。
// C3 scope 归属：只有登记该提问的会话能决策；跨会话调用按「不存在」处理（40401，不泄露他会话挂起项）。
func (s *Service) ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error {
	if !s.hasConversation(id) {
		return code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	ok, err := s.orch.ResolveElicitation(id, elicitationID, action, content)
	if err != nil {
		return code.FromError(err)
	}
	if !ok {
		return code.FromError(acl.NewError(acl.ClassRequest, "elicitation_not_found", fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil))
	}
	return nil
}

// PermissionMemories 直接透传编排器的会话级权限记忆（供 GetConversation 复用；REST 见 ListPermissionMemories）。
func (s *Service) PermissionMemories(id string) []MemoryItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PermissionMemories(id)
}

// ListPermissionMemories 校验会话存在后透传会话级权限记忆（allow/reject 双侧）。
func (s *Service) ListPermissionMemories(ctx context.Context, id string) ([]dtoconversation.MemoryResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	mems := make([]dtoconversation.MemoryResp, 0)
	for _, it := range s.PermissionMemories(id) {
		mems = append(mems, dtoconversation.MemoryResp{Title: it.Title, Kind: it.Kind, Decision: it.Decision, Since: it.Since.UTC().Format(time.RFC3339)})
	}
	return mems, nil
}

// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
func (s *Service) RevokeMemory(ctx context.Context, id, title, kind string) error {
	if !s.hasConversation(id) {
		return code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if _, err := s.orch.RevokeMemory(id, title, kind); err != nil {
		return code.FromError(err)
	}
	return nil
}

// toPermissionResp 把 manager.PermissionItem 折叠为 dtoconversation.PermissionResp。
func toPermissionResp(p PermissionItem) dtoconversation.PermissionResp {
	opts := make([]dtoconversation.PermissionOptionResp, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, dtoconversation.PermissionOptionResp{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return dtoconversation.PermissionResp{
		CallID:    p.CallID,
		SessionID: p.SessionID,
		Title:     p.Title,
		ToolCall:  p.ToolCall.ID,
		Kind:      p.ToolCall.Kind,
		Input:     p.ToolCall.Input,
		Options:   opts,
		TimeoutAt: p.TimeoutAt.UTC().Format(time.RFC3339),
	}
}

// toElicitationResp 把 manager.ElicitationItem 折叠为 dtoconversation.ElicitationResp。
func toElicitationResp(p ElicitationItem) dtoconversation.ElicitationResp {
	resp := dtoconversation.ElicitationResp{
		ElicitationID:  p.ID,
		SessionID:      p.SessionID,
		ConversationID: p.ConversationID,
		ScopeSource:    p.ScopeSource,
		Mode:           p.Mode,
		Message:        p.Message,
		TimeoutAt:      p.TimeoutAt.UTC().Format(time.RFC3339),
	}
	if p.Mode == "form" {
		resp.Schema = p.Schema
	} else {
		resp.URL = p.URL
	}
	return resp
}
