package dsh

import "strings"

// ——— B5：dsh 的原生权限投影（`DSH_PERMISSION_MODE`） ———
//
// 该变量同时决定 dsh 的 **sandbox 模式**与 **approval 策略**：仅 `danger-full-access` 时
// approval=never。平台把权限策略投影到它，形成「原生层 + 平台拦截面」的双层——**v19 起平台没有
// 策略来源**（per-agent 默认策略随 agent 面撤销），所以今天不会注入该变量；词表与端口照旧保留。
//
// 形态见 `internal/runtime/native_permission.go` 的文件头（为什么是端口而不是内核里的 switch）。

// 原生权限模式取值。
//
// **必须与 `internal/domain/interaction` 的 `NativeMode*` 常量逐字相同**（那是 wire 契约
// `permission_policy.native_mode` 的唯一事实源）。这里刻意**不** import 它：adapter 与上层之间
// 是单向依赖（上层认识 adapter，adapter 不认识上层），为几个字面量把方向弄反得不偿失。
// 同形先例：本包的 `SkillFormat` 与 `internal/core/capability.FormatSkillMDFrontmatter` 逐字相同但互不 import。
// 两侧对不上时由 `TestNativePermissionModeLiteralsMatchWire`（`internal/archtest` 的
// `knowledge_dependency_test.go`）报错，
// 而不是靠类型系统假装对得上。
const (
	NativeModeAuto           = "auto"
	NativeModeOff            = "off"
	NativeModeReadOnly       = "read-only"
	NativeModeWorkspaceWrite = "workspace-write"
	NativeModeFullAccess     = "danger-full-access"
)

// NativePermission dsh 的原生权限投影实现（结构化满足 `runtime.NativePermissionAdapter`，
// 无需 import 父包）。
type NativePermission struct{}

// Env 推导 `DSH_PERMISSION_MODE`（语义与历史实现逐字一致）：
//
//   - 显式 NativeMode（read-only/workspace-write/danger-full-access）优先，原样注入；
//   - auto + 策略 default=deny → read-only（只读沙箱，与「默认拒绝」一致）；
//   - auto + 其余（ask/allow/空）→ **不注入**（dsh 自身默认即 workspace-write；注入只会覆盖
//     操作员环境里的 DSH_PERMISSION_MODE）；
//   - 未知 mode → 不注入。
//
// 说明：策略 default=allow **不**自动升级为 danger-full-access——allow 的语义是「平台自动放行」，
// 而 danger-full-access 会同时关闭沙箱与审批；要完全放开须显式 native_mode=danger-full-access。
//
// `tools` 刻意忽略：dsh 的原生面只有"模式"这一维，工具级策略走平台的拦截面。
func (NativePermission) Env(mode, def string, tools map[string]string) ([]string, string) {
	_ = tools
	switch mode {
	case NativeModeOff:
		// 内核在调端口之前就短路了它（"不注入原生配置"是**平台级开关**，对任何底座都成立）。
		// 这里仍然显式处理：wire 枚举的任何一个取值都不该在本 adapter 里落进 `default`——
		// 那既会让行为读起来像"未知值"，也会让
		// `TestNativePermissionModeLiteralsMatchWire` 的逐字相等判定失去对象。
		return nil, "native_mode=off（不注入原生权限配置）"
	case NativeModeReadOnly, NativeModeWorkspaceWrite, NativeModeFullAccess:
		return []string{"DSH_PERMISSION_MODE=" + mode},
			"DSH_PERMISSION_MODE=" + mode + "（显式 native_mode）"
	case NativeModeAuto, "":
		if strings.ToLower(strings.TrimSpace(def)) == permissionDeny {
			return []string{"DSH_PERMISSION_MODE=" + NativeModeReadOnly},
				"DSH_PERMISSION_MODE=" + NativeModeReadOnly + "（策略 default=deny → 只读沙箱）"
		}
		d := strings.ToLower(strings.TrimSpace(def))
		if d == "" {
			d = permissionAsk
		}
		return nil, "策略 default=" + d + " → 与 dsh 默认一致，不注入（不覆盖环境值）"
	default:
		return nil, "未知 native_mode " + mode + "（跳过）"
	}
}

// 策略动作取值（与 `internal/domain/interaction.Permission*` 逐字相同；理由同上面的模式常量）。
const (
	permissionAsk  = "ask"
	permissionDeny = "deny"
)
