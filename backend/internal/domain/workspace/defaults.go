package workspace

// 工作空间**默认值**的解析（ADR-7 / v27）。
//
// 一条链，只有这一份实现：
//
//	显式（请求里写了）  >  工作空间默认（v27 的两列）  >  平台缺省（空串 = 由下游按自己的缺省处理）
//
// 为什么"平台缺省"不在这里落成具体值：能力选定的缺省是 `CapabilitySelection` 的事、
// 权限模式的缺省是权限面的事——本包把它们落死就成了**第三份**缺省，而三份缺省迟早不一致
// （表现是"同一个空请求，从 A 入口建和从 B 入口建出来的会话不一样"）。所以这一层只回答
// "有没有工作空间默认值"，空串原样往上传。
//
// 为什么不能"在 manager 里合并一次就行"：既有会话存的是**快照**，而默认值只在**建会话那一刻**
// 参与；两处各写一遍合并，改一处忘另一处的表现是"某个入口不吃工作空间默认值"——而那看起来
// 像一个全新的 bug，不像一处漏改。守卫 `TestWorkspaceDefaultsAreResolvedOnce` 钉着这一点。

// WorkspaceDefaults 一个工作空间声明的默认值（空串 = 该档没有默认值）。
type WorkspaceDefaults struct {
	// CapabilityJSON 默认能力选定（与 `sessions.capability_json` 同源同构）。
	CapabilityJSON string
	// PermissionMode 默认权限模式（取值集由权限面定义，本包只透传）。
	PermissionMode string
}

// SessionDefaults 建会话时**最终**采用的默认值（空串 = 交给下游缺省）。
type SessionDefaults struct {
	CapabilityJSON string
	PermissionMode string
}

// ResolveSessionDefaults 默认值解析的**唯一实现**：显式 > 工作空间默认 > 平台缺省。
//
// `explicit*` 是请求里**真的写了**的值；"没写"与"写了空串"在 wire 上是两件事（指针 / 缺省键），
// 由调用方区分之后把"没写"传成空串——本函数不猜"空串是不是没写"。
func ResolveSessionDefaults(explicitCapability, explicitMode string, ws *WorkspaceDefaults) SessionDefaults {
	out := SessionDefaults{CapabilityJSON: explicitCapability, PermissionMode: explicitMode}
	if ws == nil {
		return out
	}
	if out.CapabilityJSON == "" {
		out.CapabilityJSON = ws.CapabilityJSON
	}
	if out.PermissionMode == "" {
		out.PermissionMode = ws.PermissionMode
	}
	return out
}
