package acl

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"
)

// PermissionOption 是对外暴露的一条可选项（镜像 ACP PermissionOption 的可读子集）。
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// PermissionRequest 是 agent 发起的一次权限请求（宿主需向用户征询并决策）。
type PermissionRequest struct {
	SessionID string
	ToolCall  ToolCallInfo // 工具调用详情（title/kind/input）
	Options   []PermissionOption
}

// PermissionDecision 是宿主对一次权限请求的决策结果。
//   - Allowed=true：放行（若 Options 非空则选中 OptionID，缺省选第一个）；
//   - Always=true：把该请求的「action+identifier」记为会话级始终允许（后续同键自动放行）；
//   - 零值（Allowed=false, Always=false）等于拒绝/取消。
type PermissionDecision struct {
	Allowed  bool
	OptionID string
	Always   bool
}

// permissionRequestFromSDK 把 ACP RequestPermissionRequest 折叠为内部 PermissionRequest。
func permissionRequestFromSDK(p acpsdk.RequestPermissionRequest) PermissionRequest {
	req := PermissionRequest{
		SessionID: string(p.SessionId),
		ToolCall: ToolCallInfo{
			ID:     string(p.ToolCall.ToolCallId),
			Title:  firstNonEmptyPtr(p.ToolCall.Title),
			Kind:   stringPtrValue(p.ToolCall.Kind),
			Status: stringPtrValue(p.ToolCall.Status),
			Input:  p.ToolCall.RawInput,
		},
		Options: make([]PermissionOption, 0, len(p.Options)),
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, PermissionOption{
			OptionID: string(o.OptionId),
			Name:     o.Name,
			Kind:     string(o.Kind),
		})
	}
	return req
}

// permissionResponseFromDecision 把宿主的决策折叠为 ACP RequestPermissionResponse。
func permissionResponseFromDecision(d PermissionDecision, opts []acpsdk.PermissionOption) acpsdk.RequestPermissionResponse {
	if d.Allowed && len(opts) > 0 {
		oid := d.OptionID
		if oid == "" {
			oid = string(opts[0].OptionId) // 缺省选第一个选项（对应「放行」）
		}
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{
				OptionId: acpsdk.PermissionOptionId(oid),
				Outcome:  "selected",
			},
		}}
	}
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
		Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
	}}
}

// denyPermission 无条件拒绝（零值决策）。
func denyPermission(context.Context, PermissionRequest) PermissionDecision {
	return PermissionDecision{}
}

// stringPtrValue 解引用 *T 为 string（T 底层为 string；nil → 空串）。
func stringPtrValue[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}
