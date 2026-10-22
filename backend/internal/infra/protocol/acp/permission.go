package acp

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"
)

// permissionRequestFromSDK 把 ACP RequestPermissionRequest 折叠为内部 PermissionRequest。
func permissionRequestFromSDK(p acpsdk.RequestPermissionRequest) PermissionRequest {
	req := PermissionRequest{
		SessionID: string(p.SessionId),
		RequestID: requestIDFromMeta(p.Meta),
		ToolCall: ToolCallInfo{
			ID:     string(p.ToolCall.ToolCallId),
			Title:  firstNonEmptyPtr(p.ToolCall.Title),
			Kind:   stringPtrValue(p.ToolCall.Kind),
			Status: stringPtrValue(p.ToolCall.Status),
			Input:  p.ToolCall.RawInput,
		},
		Options: toPermissionOptions(p.Options),
	}
	return req
}

// permissionResponseFromDecision 把宿主的决策折叠为 ACP RequestPermissionResponse。
//
// 规则（与 PermissionDecision 注释一致）：
//   - Allowed → Selected（OptionID 有效时选中之，否则选第一个选项）；
//   - Rejected 且 OptionID 是 agent 提供的选项 → Selected{该 deny 选项}（显式拒绝可被 agent 区分）；
//   - 其余（Rejected 无可用选项 / 取消 / 超时 / 默认拒绝）→ Cancelled。
func permissionResponseFromDecision(d PermissionDecision, opts []acpsdk.PermissionOption) acpsdk.RequestPermissionResponse {
	kind, oid := EffectivePermissionOutcome(d, toPermissionOptions(opts))
	if kind == OutcomeSelected {
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

// toPermissionOptions 把 SDK 选项折叠为内部表示（EffectivePermissionOutcome 入参）。
func toPermissionOptions(opts []acpsdk.PermissionOption) []PermissionOption {
	out := make([]PermissionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, PermissionOption{
			OptionID: string(o.OptionId),
			Name:     o.Name,
			Kind:     string(o.Kind),
		})
	}
	return out
}

// hasPermissionOption 判断 opts 是否含指定 option_id。
func hasPermissionOption(opts []acpsdk.PermissionOption, oid string) bool {
	if oid == "" {
		return false
	}
	for _, o := range opts {
		if string(o.OptionId) == oid {
			return true
		}
	}
	return false
}

// denyPermission 无条件拒绝（零值决策 → Cancelled）。
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
