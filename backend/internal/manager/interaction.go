package manager

// ——— 会话交互面的**剩余形状**（P4a 正文第四十刀之后）———
//
// 只读四条（`GetSession` / `ListPendingPermissions` / `ListPermissionMemories` /
// `ListPendingElicitations`）已收进 `service/svcsession`（`DetailSvc` + `InteractionSource`），
// 连同两个投影 `toPermissionResp` / `toElicitationResp` 一起搬走——投影只该有一份。
//
// 本文件此后只剩两样：
//
//	① 三个**事实出口**（`PendingPermissions` / `PermissionMemories` / `PendingElicitations`）：
//	   它们是 `svcsession.InteractionSource` 的实现（那两份状态归执行面持有，用例层只认这三条窄端口）；
//	② 三条**动作面**（`ResolvePermission` / `ResolveElicitation` / `RevokeMemory`）：它们要走
//	   `Orchestrator` 把决策**投递给底座**，属于编排，随 `Orchestrator` 在 P5 一起收口
//	   （§3.1.58 的结论：编排既不是执行面、也不是「读面」）。

import (
	"context"
	"fmt"
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
)

// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回**实际投递给 agent 的回执**
// （allowed/rejected/always/请求 option_id + ACP 形态 outcome/selected_option_id），
// 供 REST 如实回执——不再回显请求参数（option_id 可能被兜底为第一个选项或降级 Cancelled）。
//
// C3 scope 归属：只有登记该请求的会话能决策；跨会话调用（陈旧标签页/串号请求）按「不存在」处理
// （40401，不泄露他会话挂起项）——与 ResolveElicitation 同一立场。
func (s *Service) ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*dtosession.PermissionDecisionResp, error) {
	if !s.hasSession(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
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
	return &dtosession.PermissionDecisionResp{
		CallID:           callID,
		Allowed:          out.Allowed,
		Rejected:         out.Rejected,
		Always:           out.Always,
		OptionID:         out.OptionID,
		Outcome:          out.Kind,
		SelectedOptionID: out.Selected,
	}, nil
}

// PendingPermissions 直接透传编排器的挂起权限（供 GetSession/ListPendingPermissions 复用）。
func (s *Service) PendingPermissions(id string) []interaction.PermissionItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingPermissions(id)
}

// PendingElicitations 直接透传编排器的挂起结构化提问（供 ListPendingElicitations 复用）。
func (s *Service) PendingElicitations(id string) []interaction.ElicitationItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingElicitations(id)
}

// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel；accept 可带 content）。
// C3 scope 归属：只有登记该提问的会话能决策；跨会话调用按「不存在」处理（40401，不泄露他会话挂起项）。
func (s *Service) ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error {
	if !s.hasSession(id) {
		return code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
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

// PermissionMemories 直接透传编排器的会话级权限记忆（供 GetSession 复用；REST 见 ListPermissionMemories）。
func (s *Service) PermissionMemories(id string) []interaction.MemoryItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PermissionMemories(id)
}

// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
func (s *Service) RevokeMemory(ctx context.Context, id, title, kind string) error {
	if !s.hasSession(id) {
		return code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if s.orch == nil {
		return code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if _, err := s.orch.RevokeMemory(id, title, kind); err != nil {
		return code.FromError(err)
	}
	return nil
}
