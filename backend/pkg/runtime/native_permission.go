package runtime

// ——— B5：原生权限投影端口（第二张可执行端口表） ———
//
// 背景：平台的权限策略有两层——**拦截面**（ACP `session/request_permission` 到达时先按策略裁决）
// 与**底座原生面**（沙箱模式 / 审批策略 / 工具级 permission 表）。两者不联动时会出现：
//   - 底座在「根本没问」的情况下执行工具（拦截面看不到，策略无从生效）；
//   - 底座的沙箱比策略更严/更松（例如策略 allow 了写文件，但底座沙箱仍拒绝工作区外写入）。
//
// B5 在**启动时**把策略投影到底座的原生开关（不改变拦截面的存在，形成双层）。
//
// **为什么是一个端口而不是内核里的 switch**（2026-10-12 收口）：映射用的词表是**该底座的私有知识**
// ——`DSH_PERMISSION_MODE` 的取值、opencode 的 `bash/edit/webfetch/external_directory` 动作名、
// 内联配置是 `{"permission":{…}}` 这一层壳。写在内核里的后果不是"多一处脏"，而是**接一个新 runtime
// 必须回内核改 switch**（那正是"没有 adapter 层"的定义）。
//
// 形状与 `resource.Syncer` 同款（ADR-2）：`Spec` 只装纯数据，**可执行对象挂注册表第二张可执行端口表**。
//
// **签名刻意只用内建类型**（`string` / `map[string]string`）：接口定义在父包 `pkg/runtime`，而
// adapter 子包不能 import 父包（约束⑦的父子 import 环）。用内建类型后 adapter 只要写出**同签名的方法**
// 就结构化满足本接口，无需 import 任何父包类型。
type NativePermissionAdapter interface {
	// Env 由平台策略推导该底座的**原生权限环境变量**（叠加到子进程 env）。
	//
	//	mode  —— 归一后的原生模式：auto | read-only | workspace-write | danger-full-access
	//	         （`off` 由平台在调用前短路——"不注入原生配置"是平台级开关，不属于任何底座）
	//	def   —— 策略默认动作：allow | ask | deny（空 = 未声明；**未归一**，由实现按自己的词表处理）
	//	tools —— 工具/动作名 → allow | ask | deny（**未归一**，同上）
	//
	// 返回 nil/空 = 不注入；desc 是人类可读说明（写日志与诊断）。**不注入也要给 desc**：
	// "为什么没注入"与"注入了什么"一样要看得到，否则"我设了策略却没反应"只能靠猜。
	Env(mode, def string, tools map[string]string) (env []string, desc string)
}

// RegisterNativePermission 登记某 runtime 的原生权限投影实现（同 ID 后注册覆盖）。
func (r *Registry) RegisterNativePermission(id string, a NativePermissionAdapter) {
	if r == nil {
		return
	}
	if r.nativePermissions == nil {
		r.nativePermissions = map[string]NativePermissionAdapter{}
	}
	r.nativePermissions[id] = a
}

// NativePermission 取某 runtime 的原生权限实现。
//
// `ok=false` = 该底座**没有**原生权限机制（不是"未登记"）：内核据此如实报
// 「该 runtime 无原生权限开关（仅平台拦截面）」，而不是静默什么都不做。
func (r *Registry) NativePermission(id string) (NativePermissionAdapter, bool) {
	if r == nil || r.nativePermissions == nil {
		return nil, false
	}
	a, ok := r.nativePermissions[id]
	return a, ok && a != nil
}
