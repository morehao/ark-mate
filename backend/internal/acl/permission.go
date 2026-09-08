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

// PermissionDecision 是宿主对一次权限请求的决策结果（决策 v2 语义，对齐 ACP Selected/Cancelled）：
//   - Allowed=true：放行（若 Options 非空则选中 OptionID，缺省选第一个）；
//   - Rejected=true：显式拒绝。OptionID 为所选 deny 选项时映射 ACP Selected{deny 选项}（agent 可区分
//     拒绝与取消）；OptionID 为空或不在 agent 提供的选项内时兜底 Cancelled；
//   - Allowed 与 Rejected 皆 false：取消/超时/默认拒绝，一律映射 ACP Cancelled；
//   - Always=true：把该请求的「action+identifier」记为会话级记忆（allow_always / reject_always
//     按 Allowed/Rejected 落到允许或拒绝侧，后续同键请求在 AutoDecision 快路径自动决策）。
type PermissionDecision struct {
	Allowed  bool
	Rejected bool
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
//
// 规则（与 PermissionDecision 注释一致）：
//   - Allowed → Selected（OptionID 有效时选中之，否则选第一个选项）；
//   - Rejected 且 OptionID 是 agent 提供的选项 → Selected{该 deny 选项}（显式拒绝可被 agent 区分）；
//   - 其余（Rejected 无可用选项 / 取消 / 超时 / 默认拒绝）→ Cancelled。
func permissionResponseFromDecision(d PermissionDecision, opts []acpsdk.PermissionOption) acpsdk.RequestPermissionResponse {
	selected := func(oid string) acpsdk.RequestPermissionResponse {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{
				OptionId: acpsdk.PermissionOptionId(oid),
				Outcome:  "selected",
			},
		}}
	}
	cancelled := func() acpsdk.RequestPermissionResponse {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
			Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
		}}
	}
	switch {
	case d.Allowed:
		if len(opts) == 0 {
			return cancelled() // 无可用选项时无法 Selected（同历史行为：一律取消）
		}
		oid := d.OptionID
		if !hasPermissionOption(opts, oid) {
			oid = string(opts[0].OptionId) // 缺省选第一个选项（对应「放行」）
		}
		return selected(oid)
	case d.Rejected:
		if d.OptionID != "" && hasPermissionOption(opts, d.OptionID) {
			return selected(d.OptionID) // 显式拒绝且 agent 提供了该 deny 选项 → Selected
		}
		return cancelled() // 无 deny 选项可回执 → 兜底 Cancelled
	default:
		return cancelled()
	}
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
