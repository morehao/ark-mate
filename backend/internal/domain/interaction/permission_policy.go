package interaction

import (
	"fmt"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"strings"
)

// ——— 权限策略：**类型与词表**（M2 #7 引入；v19 起平台没有策略来源） ———
//
// 这些类型今天**不驱动任何注入**：per-agent 默认策略随 agent 面在 v19 撤销，`nativePermissionEnv`
// 因此没有生产调用点（见 native_permission.go 的说明）。保留它们的理由是 `native_mode` 的取值
// 仍是**对外 wire 契约**（`permission_policy.native_mode`：定义文件 / REST 请求 / 前端表单），
// 由 TestNativePermissionModeLiteralsMatchWire 与 adapter 侧逐字钉住。
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

// PermissionPolicy 定义文件里的 `permission_policy` 字段形态（v19 起平台不再有策略来源，
// 见本文件头；类型保留为契约锚点）。
type PermissionPolicy struct {
	// Default 未匹配到具体工具时的动作：ask（缺省）| allow | deny。
	Default string `json:"default,omitempty"`
	// Tools 工具粒度动作：键 = 工具 title 或 ACP kind，值 = ask | allow | deny。
	Tools map[string]string `json:"tools,omitempty"`
	// NativeMode 原生权限模式（B5）：""/"auto" = 按 Default 推导；"off" = 不注入；
	// "read-only"|"workspace-write"|"danger-full-access" = 显式指定。
	// 仅对声明了原生开关的 runtime 生效（各底座的开关形态与认哪些取值，见它自己的 adapter：
	// `pkg/runtime/<id>/native_permission.go`；没声明原生面的底座收不到任何注入）。
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
func autoPermissionDecision(action string, opts []protocol.PermissionOption) (protocol.PermissionDecision, bool) {
	want := ""
	switch action {
	case PermissionAllow:
		want = permKindAllowOnce
	case PermissionDeny:
		want = permKindRejectOnce
	default:
		return protocol.PermissionDecision{}, false
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
			return protocol.PermissionDecision{}, false
		}
		return protocol.PermissionDecision{Allowed: true, OptionID: id}, true
	}
	id, ok := pick("reject")
	if !ok {
		return protocol.PermissionDecision{}, false
	}
	return protocol.PermissionDecision{Rejected: true, OptionID: id}, true
}
