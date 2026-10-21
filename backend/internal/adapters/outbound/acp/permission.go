package acp

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
	// RequestID 是 wire `_meta` 给出的请求归属键（requestId/request_id；无则空串）。
	// 宿主的待决键是自生成的 call_id（genCallID），故它不参与决策寻址，只作**归属证据**：
	// 排障时与 agent 侧日志对齐同一次 JSON-RPC 请求（会话外 request-scope 的请求没有 sessionId，
	// 只有它能对上号）。容忍读取见 internal/adapters/outbound/acp/scope.go。
	RequestID string
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

// ACP 回执形态（对外可观测；与 acpsdk 的 outcome 字段同字面值）。
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

// EffectivePermissionOutcome 返回决策**实际投递给 agent** 的回执形态与有效 option_id
// （单一事实源：permissionResponseFromDecision 与 REST 决策回执共用，避免两处规则漂移）。
//   - Allowed 且 agent 提供了选项 → (selected, OptionID 有效则用之，否则第一个选项)；
//   - Allowed 但无选项 → (cancelled, "")（无法 Selected）；
//   - Rejected 且 OptionID 是 agent 提供的选项 → (selected, 该选项)（显式拒绝，agent 可区分）；
//   - 其余（Rejected 无 deny 选项 / 取消 / 超时）→ (cancelled, "")。
func EffectivePermissionOutcome(d PermissionDecision, opts []PermissionOption) (kind, optionID string) {
	has := func(oid string) bool {
		if oid == "" {
			return false
		}
		for _, o := range opts {
			if o.OptionID == oid {
				return true
			}
		}
		return false
	}
	switch {
	case d.Allowed:
		if len(opts) == 0 {
			return OutcomeCancelled, ""
		}
		if has(d.OptionID) {
			return OutcomeSelected, d.OptionID
		}
		return OutcomeSelected, opts[0].OptionID
	case d.Rejected:
		if has(d.OptionID) {
			return OutcomeSelected, d.OptionID
		}
		return OutcomeCancelled, ""
	default:
		return OutcomeCancelled, ""
	}
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
