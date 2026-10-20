package interaction

import (
	"time"

	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// ——— 权限面的**领域值类型**（C2：底座协议不再进入本域）———
//
// 这些形状此前是 `infra/acp` 的类型。它们描述的是"一次权限征询与它的决策"——**平台语义**，
// 不是 ACP wire：底座协议里的对应结构在连接边界被翻译成这里的值（`runtimeconn` 的调用方）。
//
// `PermissionItem` 的字段带 json tag，是因为它**就是** REST/WS 载荷（挂起权限列表）：
// 标签与 wire 逐字一致，改 tag 等于改前端契约。

// PermissionOption 是 agent 提供的一个可选项（用户界面上的按钮）。
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	// Kind 选项类别：allow_once | allow_always | reject_once | reject_always（透传）。
	Kind string `json:"kind"`
}

// PermissionRequest 是一次权限征询。
type PermissionRequest struct {
	SessionID string
	// RequestID 是 wire 给出的请求归属键（无则空串）。
	//
	// 宿主的待决键是自生成的 call_id，故它不参与决策寻址，只作**归属证据**：排障时与 agent
	// 侧日志对齐同一次请求（会话外 request-scope 的请求没有 sessionId，只有它能对上号）。
	RequestID string
	// ToolCall 工具调用详情（title/kind/input）。
	ToolCall event.ToolCall
	Options  []PermissionOption
}

// PermissionDecision 是一次权限决策（决策 v2 语义）：
//   - Allowed：放行（若 Options 非空则选中 OptionID，缺省选第一个）；
//   - Rejected：显式拒绝。OptionID 为所选 deny 选项时映射「选中该选项」（agent 可区分拒绝与取消）；
//     OptionID 为空或不在选项内时兜底「取消」；
//   - 两者皆 false：取消/超时/默认拒绝，一律映射「取消」；
//   - Always：把该请求的 action+identifier 记为会话级记忆（allow_always / reject_always
//     按 Allowed/Rejected 落到允许或拒绝侧，后续同键请求走 AutoDecision 快路径）。
type PermissionDecision struct {
	Allowed  bool
	Rejected bool
	OptionID string
	Always   bool
}

// 决策的**实际回执**形态（对外 `outcome` 字段的取值）。
const (
	// OutcomeSelected 投递给 agent 的是一个选中的选项。
	OutcomeSelected = "selected"
	// OutcomeCancelled 投递的是取消（没有选中项）。
	OutcomeCancelled = "cancelled"
)

// EffectivePermissionOutcome 计算一次决策的**实际回执**（kind + 真正选中的 option_id）。
//
// 规则与 ACP 层同一份（"用户点了允许"与"agent 收到什么"是两件事：选项可能被兜底成第一个、
// 或整条降级成取消）。两份实现必然并存（领域不能 import 协议库、协议库也不该认识领域），
// 由 archtest 的 `TestEffectivePermissionOutcomeParity` 用一张决策×选项矩阵逐格比对。
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

// PermissionItem 是一条挂起权限请求的对外表示（REST/WS 载荷）。
type PermissionItem struct {
	CallID    string             `json:"call_id"`
	SessionID string             `json:"session_id"`
	Title     string             `json:"title"`
	ToolCall  event.ToolCall     `json:"tool_call"`
	Options   []PermissionOption `json:"options"`
	TimeoutAt time.Time          `json:"timeout_at"`
}
