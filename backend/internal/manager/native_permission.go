package manager

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime/deepseekharness"
	"github.com/morehao/ark-mate/pkg/runtime/opencode"
)

// ——— B5：原生权限映射（platform permission_policy → runtime 原生权限开关） ———
//
// 背景：平台当前的权限策略是**拦截面**（ACP session/request_permission 到达时先按策略裁决）。
// 但底座自身也有原生权限机制（沙箱模式、审批策略、工具级 permission 表），两者不联动时会出现：
//   - 底座在「根本没问」的情况下执行工具（拦截面看不到，策略无从生效）；
//   - 底座的沙箱比策略更严/更松（例如策略 allow 了写文件，但底座沙箱仍拒绝工作区外写入）。
//
// B5 在**启动时**把策略投影到底座的原生开关（不改变拦截面的存在，形成双层）：
//   - deepseekharness：`DSH_PERMISSION_MODE=read-only|workspace-write|danger-full-access`
//     （该变量同时决定 sandbox 模式与 approval 策略：仅 danger-full-access 时 approval=never）；
//   - opencode：`OPENCODE_CONFIG_CONTENT={"permission":{...}}`（进程内配置覆盖，不落用户配置文件；
//     `opencode debug config` 可验证生效）。
//
// **不注入的三种情况**（避免无谓覆盖/覆盖操作员环境）：
//   - 未声明策略（nil）——行为与历史一致；
//   - 推导结果与底座自身默认等价（dsh: auto+非 deny → workspace-write；opencode: 全 ask）——
//     此时注入只会覆盖操作员在环境/全局配置里设的默认值，收益为零；
//   - `native_mode: off`。
//
// 底座无原生权限机制（adapter 未声明原生权限 env）→ 不注入（说明中写明降级原因）。

// 原生权限模式取值（PermissionPolicy.NativeMode）。
const (
	// NativeModeAuto 按策略 Default 推导（默认值；空字符串等价）。
	NativeModeAuto = "auto"
	// NativeModeOff 不注入原生配置（底座走自身默认）。
	NativeModeOff = "off"
	// NativeModeReadOnly 只读沙箱（dsh: read-only）。
	NativeModeReadOnly = "read-only"
	// NativeModeWorkspaceWrite 工作区可写（dsh: workspace-write，即 dsh 自身默认）。
	NativeModeWorkspaceWrite = "workspace-write"
	// NativeModeFullAccess 完全访问（dsh: danger-full-access → approval=never，沙箱关闭）。
	NativeModeFullAccess = "danger-full-access"
)

// ValidNativeModes 合法取值（校验与错误提示用）。
var ValidNativeModes = []string{NativeModeAuto, NativeModeOff, NativeModeReadOnly, NativeModeWorkspaceWrite, NativeModeFullAccess}

// opencodeActionAliases 平台策略键 → opencode permission 键。
//
// 平台策略键是「工具 title 或 ACP kind」（bash/edit/read/execute/write/fetch…），
// opencode 的 permission 键是动作名（bash/edit/read/webfetch/external_directory/*）。
// 只映射确定等价的键；其余键**不猜测**（跳过并记日志，交回平台拦截面裁决）。
var opencodeActionAliases = map[string]string{
	"*":          "*",
	"bash":       "bash",
	"shell":      "bash",
	"execute":    "bash",
	"edit":       "edit",
	"write":      "edit",
	"read":       "read",
	"fetch":      "webfetch",
	"webfetch":   "webfetch",
	"web":        "webfetch",
	"grep":       "read",
	"glob":       "read",
	"list":       "read",
	"external":   "external_directory",
	"directory":  "external_directory",
	"permission": "*",
}

// nativePermissionEnv 计算该 runtime + 策略下的原生权限环境变量。
// 返回 nil = 不注入；desc 为人类可读说明（日志/诊断用）。
func nativePermissionEnv(runtimeID string, p *PermissionPolicy) (env []string, desc string) {
	if p == nil {
		return nil, ""
	}
	mode := strings.ToLower(strings.TrimSpace(p.NativeMode))
	if mode == NativeModeOff {
		return nil, "native_mode=off（不注入原生权限配置）"
	}

	switch runtimeID {
	case deepseekharness.ID:
		dm, why, inject := dshPermissionMode(p, mode)
		if !inject {
			return nil, why
		}
		return []string{"DSH_PERMISSION_MODE=" + dm}, "DSH_PERMISSION_MODE=" + dm + "（" + why + "）"
	case opencode.ID:
		cfg, skipped := opencodePermissionConfig(p)
		if len(cfg) == 0 {
			return nil, "无可映射的 opencode 权限项（策略为空）"
		}
		// 只有「全 ask」一项：与 opencode 默认等价，注入无收益且会覆盖操作员的全局配置。
		if len(cfg) == 1 && cfg["*"] == PermissionAsk {
			return nil, "策略等价 opencode 默认（全 ask），不注入"
		}
		raw, err := json.Marshal(map[string]any{"permission": cfg})
		if err != nil {
			return nil, "opencode 权限配置序列化失败：" + err.Error()
		}
		note := "OPENCODE_CONFIG_CONTENT permission=" + compactJSON(cfg)
		if len(skipped) > 0 {
			note += "（未映射键 " + strings.Join(skipped, ",") + "，仍由平台拦截面裁决）"
		}
		return []string{"OPENCODE_CONFIG_CONTENT=" + string(raw)}, note
	default:
		return nil, "runtime " + runtimeID + " 无原生权限开关（仅平台拦截面）"
	}
}

// dshPermissionMode 推导 dsh 的 DSH_PERMISSION_MODE。
//
// 规则（保守）：
//   - 显式 NativeMode（read-only/workspace-write/danger-full-access）优先，原样注入；
//   - auto + 策略 default=deny → read-only（只读沙箱，与「默认拒绝」一致）；
//   - auto + 其余（ask/allow/空）→ **不注入**（dsh 自身默认即 workspace-write；注入只会覆盖
//     操作员环境里的 DSH_PERMISSION_MODE）。
//
// 说明：策略 default=allow **不**自动升级为 danger-full-access——allow 的语义是「平台自动放行」，
// 而 danger-full-access 会同时关闭沙箱与审批；要完全放开须显式 native_mode=danger-full-access。
func dshPermissionMode(p *PermissionPolicy, mode string) (string, string, bool) {
	switch mode {
	case NativeModeReadOnly, NativeModeWorkspaceWrite, NativeModeFullAccess:
		return mode, "显式 native_mode", true
	case NativeModeAuto, "":
		if strings.ToLower(strings.TrimSpace(p.Default)) == PermissionDeny {
			return NativeModeReadOnly, "策略 default=deny → 只读沙箱", true
		}
		def := strings.ToLower(strings.TrimSpace(p.Default))
		if def == "" {
			def = PermissionAsk
		}
		return NativeModeWorkspaceWrite, "策略 default=" + def + " → 与 dsh 默认一致，不注入（不覆盖环境值）", false
	default:
		return "", "未知 native_mode " + mode + "（跳过）", false
	}
}

// opencodePermissionConfig 由策略生成 opencode permission 表（键 = 动作，值 = ask|allow|deny）。
// 默认项写成 "*"；工具项按别名归一；无法映射的键返回在 skipped 中（不猜测）。
func opencodePermissionConfig(p *PermissionPolicy) (cfg map[string]string, skipped []string) {
	cfg = map[string]string{}
	if d := normalizeAction(p.Default); d != "" {
		cfg["*"] = d
	}
	for k, v := range p.Tools {
		action := normalizeAction(v)
		if action == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		mapped, ok := opencodeActionAliases[key]
		if !ok {
			skipped = append(skipped, k)
			continue
		}
		// 显式工具项优先于默认项；同一动作多键映射时 deny > ask > allow（更严者胜）。
		if cur, exists := cfg[mapped]; exists && actionRank(cur) >= actionRank(action) {
			continue // 已有更严（或同等）动作：保留
		}
		cfg[mapped] = action
	}
	sort.Strings(skipped)
	return cfg, skipped
}

// actionRank 越严越大（用于同键冲突时取更严者）。
func actionRank(a string) int {
	switch a {
	case PermissionDeny:
		return 3
	case PermissionAsk:
		return 2
	case PermissionAllow:
		return 1
	default:
		return 0
	}
}

// normalizeAction 归一化策略动作（空/未知 → ""）。
func normalizeAction(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case PermissionAllow:
		return PermissionAllow
	case PermissionAsk:
		return PermissionAsk
	case PermissionDeny:
		return PermissionDeny
	default:
		return ""
	}
}

// compactJSON 稳定序输出小 JSON（日志用）。
func compactJSON(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// nativePermissionEnvFor 返回该 runtime + 策略下的原生权限 env 并记一行日志（仅实际注入时）。
// 开关关闭（MATE_NATIVE_PERMISSION=off）→ 恒返回 nil。
func (s *Service) nativePermissionEnvFor(runtimeID string, p *PermissionPolicy) []string {
	if !s.NativePermissionEnabled() {
		return nil
	}
	extra, desc := nativePermissionEnv(runtimeID, p)
	if len(extra) == 0 {
		return nil
	}
	slog.Default().Info("原生权限映射", "runtime", runtimeID, "detail", desc)
	return extra
}
