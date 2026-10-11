package manager

import (
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/pkg/acl"
)

// ——— per-agent 默认权限策略（M2 #7 / §4.5 权限位） ———
//
// 语义：策略是**运维护栏**，在 ACP 权限请求到达时先于会话级记忆判定：
//   - allow → 自动放行（选 allow_once，不写会话记忆，避免污染用户决策）；
//   - deny  → 自动拒绝（选 reject_once）；
//   - ask   → 不决定，回落既有路径（会话级记忆 → 发布挂起请求等用户决策）。
//
// 未声明策略（nil）= 全 ask = 行为与历史一致（不回归）。
// 匹配键：先按工具 title（如 bash/write），再按 ACP kind（execute/edit/read/…），最后 default。
// 键与值均大小写不敏感（去除首尾空白）。

// 策略动作取值。
const (
	PermissionAsk   = "ask"
	PermissionAllow = "allow"
	PermissionDeny  = "deny"
)

// PermissionPolicy 每 agent 的默认权限策略（定义文件 permission_policy 字段）。
type PermissionPolicy struct {
	// Default 未匹配到具体工具时的动作：ask（缺省）| allow | deny。
	Default string `json:"default,omitempty"`
	// Tools 工具粒度动作：键 = 工具 title 或 ACP kind，值 = ask | allow | deny。
	Tools map[string]string `json:"tools,omitempty"`
	// NativeMode 原生权限模式（B5）：""/"auto" = 按 Default 推导；"off" = 不注入；
	// "read-only"|"workspace-write"|"danger-full-access" = 显式指定。
	// 仅对声明了原生开关的 runtime 生效（当前 dsh 的 DSH_PERMISSION_MODE；
	// opencode 用本策略的 default/tools 生成 permission 表，mode 只认 off）。
	NativeMode string `json:"native_mode,omitempty"`
}

// Validate 校验策略取值（未知动作值报错，避免「写了但不生效」的静默失败）。
func (p *PermissionPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if err := validatePermissionAction(p.Default); err != nil {
		return fmt.Errorf("permission_policy.default: %w", err)
	}
	if err := validateNativeMode(p.NativeMode); err != nil {
		return fmt.Errorf("permission_policy.native_mode: %w", err)
	}
	for k, v := range p.Tools {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("permission_policy.tools: 存在空键")
		}
		if err := validatePermissionAction(v); err != nil {
			return fmt.Errorf("permission_policy.tools[%q]: %w", k, err)
		}
	}
	return nil
}

func validatePermissionAction(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", PermissionAsk, PermissionAllow, PermissionDeny:
		return nil
	default:
		return fmt.Errorf("动作 %q 非法（ask|allow|deny）", v)
	}
}

// validateNativeMode 校验 native_mode 取值（空 = auto）。
func validateNativeMode(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", NativeModeAuto, NativeModeOff, NativeModeReadOnly, NativeModeWorkspaceWrite, NativeModeFullAccess:
		return nil
	default:
		return fmt.Errorf("取值 %q 非法（%s）", v, strings.Join(ValidNativeModes, "|"))
	}
}

// Decide 解析该工具的动作（ask|allow|deny；未匹配且无 default → ask）。
func (p *PermissionPolicy) Decide(title, kind string) string {
	if p == nil {
		return PermissionAsk
	}
	if v, ok := lookupAction(p.Tools, title); ok {
		return v
	}
	if v, ok := lookupAction(p.Tools, kind); ok {
		return v
	}
	if a := strings.ToLower(strings.TrimSpace(p.Default)); a == PermissionAllow || a == PermissionDeny {
		return a
	}
	return PermissionAsk
}

// lookupAction 大小写不敏感查表（返回归一化动作；命中 ask 也算命中）。
func lookupAction(m map[string]string, key string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" || len(m) == 0 {
		return "", false
	}
	for mk, mv := range m {
		if strings.ToLower(strings.TrimSpace(mk)) != k {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(mv)) {
		case PermissionAllow:
			return PermissionAllow, true
		case PermissionDeny:
			return PermissionDeny, true
		default:
			return PermissionAsk, true
		}
	}
	return "", false
}

// autoPermissionDecision 把策略动作翻译成 ACP 决策：
//   - allow → 优先 allow_once（回落任意 allow_* 选项）；
//   - deny  → 优先 reject_once（回落任意 reject_* 选项）。
//
// 找不到对应选项时返回 ok=false（调用方回落征询用户，不臆造选项）。
func autoPermissionDecision(action string, opts []acl.PermissionOption) (acl.PermissionDecision, bool) {
	want := ""
	switch action {
	case PermissionAllow:
		want = permKindAllowOnce
	case PermissionDeny:
		want = permKindRejectOnce
	default:
		return acl.PermissionDecision{}, false
	}
	pick := func(prefix string) (string, bool) {
		for _, o := range opts {
			if o.Kind == want {
				return o.OptionID, true
			}
		}
		for _, o := range opts {
			if strings.HasPrefix(o.Kind, prefix) {
				return o.OptionID, true
			}
		}
		return "", false
	}
	if action == PermissionAllow {
		id, ok := pick("allow")
		if !ok {
			return acl.PermissionDecision{}, false
		}
		return acl.PermissionDecision{Allowed: true, OptionID: id}, true
	}
	id, ok := pick("reject")
	if !ok {
		return acl.PermissionDecision{}, false
	}
	return acl.PermissionDecision{Rejected: true, OptionID: id}, true
}

// policyAction 返回该会话所属 agent 对本次权限请求的既定动作；
// ok=false 表示无 agent/无策略/动作为 ask（调用方走既有征询路径）。
func (o *Orchestrator) policyAction(sessionID string, req acl.PermissionRequest) (string, string, bool) {
	agentID := o.svc.SessionAgentID(sessionID)
	if agentID == "" {
		return "", "", false
	}
	agent, ok := o.svc.AgentByID(agentID)
	if !ok || agent.PermissionPolicy == nil {
		return "", "", false
	}
	action := agent.PermissionPolicy.Decide(req.ToolCall.Title, req.ToolCall.Kind)
	if action == PermissionAllow || action == PermissionDeny {
		return action, agentID, true
	}
	return "", "", false
}

// toPermissionPolicyResp 内部策略 → 对外表示（nil → nil，缺省不出现字段）。
func toPermissionPolicyResp(p *PermissionPolicy) *dtoagent.PermissionPolicyResp {
	if p == nil {
		return nil
	}
	return &dtoagent.PermissionPolicyResp{Default: p.Default, Tools: p.Tools, NativeMode: p.NativeMode}
}

// fromPermissionPolicyResp 对外表示 → 内部策略（nil → nil）。
func fromPermissionPolicyResp(p *dtoagent.PermissionPolicyResp) *PermissionPolicy {
	if p == nil {
		return nil
	}
	return &PermissionPolicy{Default: p.Default, Tools: p.Tools, NativeMode: p.NativeMode}
}
