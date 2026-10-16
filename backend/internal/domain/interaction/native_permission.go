package interaction

import (
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— B5：原生权限映射（platform permission_policy → runtime 原生权限开关） ———
//
// 背景：平台当前的权限策略是**拦截面**（ACP session/request_permission 到达时先按策略裁决）。
// 但底座自身也有原生权限机制（沙箱模式、审批策略、工具级 permission 表），两者不联动时会出现：
//   - 底座在「根本没问」的情况下执行工具（拦截面看不到，策略无从生效）；
//   - 底座的沙箱比策略更严/更松（例如策略 allow 了写文件，但底座沙箱仍拒绝工作区外写入）。
//
// B5 在**启动时**把策略投影到底座的原生开关（不改变拦截面的存在，形成双层）。
//
// **本文件不认识任何具体底座**（2026-10-12 收口）：原生开关的变量名、哪些工具动作名、内联配置
// 长什么壳，都是**该底座的私有知识**，实现住在各自的 adapter 包里（`pkg/runtime/<id>/`），
// 按 ADR-2 挂在 `runtime.Registry` 的第二张表上（见 `pkg/runtime/native_permission.go`）。
// 内核只做两件事：把平台策略翻译成中性入参、把端口吐出的 env 交出去。
//
// **不注入的三种情况**（避免无谓覆盖/覆盖操作员环境）：
//   - 未声明策略（nil）——行为与历史一致；
//   - 推导结果与底座自身默认等价（**各 adapter 自己判定**，判据是该底座自己的默认行为）；
//   - `native_mode: off`——**平台级开关**，对任何底座都成立，所以在调端口**之前**短路。
//
// 底座无原生权限机制（注册表里没有该 runtime 的实现）→ 不注入，说明中写明降级原因。

// 原生权限模式取值（PermissionPolicy.NativeMode）。
//
// 它是**wire 契约**（`permission_policy.native_mode`：定义文件 / REST 请求 / 前端表单），
// 因此事实源在内核这一侧；adapter 侧的同名字面量由 `TestNativePermissionModeLiteralsMatchWire` 钉住逐字一致。
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

// NativePermissionEnv 计算该 runtime + 策略下的原生权限环境变量。
// 返回 nil = 不注入；desc 为人类可读说明（日志/诊断用）。
//
// v19 起**平台不再有策略来源**（per-agent 默认权限策略随 agent 面删除）：本函数因此没有被
// 生产调用点，只作为端口契约的实现与词表锚点保留——`native_mode` 的取值仍是对外 wire 契约
// （由 `TestNativePermissionModeLiteralsMatchWire` 与 adapter 侧逐字钉住），把它一起删掉
// 等于把"这里曾经有过一个原生权限面"这件事也删掉，下一个人要接会话级策略时会从零重猜一遍。
// 上游若有策略（未来的会话级 runtime 配置），注入点仍是这里。
//
// reg 是**产品装配的那张注册表**：端口在那里（`ProductRegistry` 只装配构建期选中的那一个），
// 因此未选中的底座天然得到「无原生权限开关」这条如实结论。
func NativePermissionEnv(reg *runtime.Registry, runtimeID string, p *PermissionPolicy) (env []string, desc string) {
	if p == nil {
		return nil, ""
	}
	mode := strings.ToLower(strings.TrimSpace(p.NativeMode))
	if mode == NativeModeOff {
		// `off` 是**平台级开关**（"不注入原生配置"对任何底座都成立），不是某个底座的词表，
		// 所以在这里短路，而不是让每个 adapter 各实现一遍。
		return nil, "native_mode=off（不注入原生权限配置）"
	}
	adapter, ok := reg.NativePermission(runtimeID)
	if !ok || adapter == nil {
		return nil, "runtime " + runtimeID + " 无原生权限开关（仅平台拦截面）"
	}
	// 动作取值与工具键**不在这里归一**：那是该底座的词表（键名与别名规则都不一样），
	// 属于 adapter 的知识；内核只如实转发。
	return adapter.Env(mode, p.Default, p.Tools)
}
