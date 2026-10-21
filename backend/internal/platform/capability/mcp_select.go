package capability

// ——— MCP 定义的选址语义（M7-2：注入面与网关面共用**唯一**实现） ———
//
// 这段逻辑原先只活在 manager（`mcpSelectionFor`）。M7 之后多了一个消费者：平台侧 stdio 网关
// （`internal/adapters/inbound/mcpgateway`）必须按**同一套**语义算出"这个 agent×runtime 该看到哪些 server"。
// 两边各写一遍的后果不是报错，而是**静默不同面**：网关漏了 enabled 判据 → 用户停用的 server
// 依然能被模型调；网关漏了会话白名单 → 钉过选定的会话看到 agent 的全量。
//
// 故选址落在 `pkg/capability`（可复用层）：它只看定义本身，不认识会话/agent/运行时的类型；
// "白名单从哪来"（会话选定 > agent 声明）仍归调用方——那是**优先级**，不是**选择**。

// SelectMcpServers 按「启用开关 ∩ 传输承载 ∩ id 白名单」选出候选定义，顺序与入参一致。
//
// 三个判据缺一不可，且都有明确语义：
//   - `IsEnabled()`：禁用 = **不注入**（定义、密钥引用、探测结果保留）。这是 override 语义，
//     不是删除；任何"看得到定义但看不见工具"的差异都必须能追到这一条。
//   - `transports`：该消费方**能承载**的传输。注意它有两种完全不同的用法——注入面传"该 runtime
//     声明的传输"，网关传 `RemoteMcpTransports()`（远程那三种）。**不要**在这里加默认值：
//     "没传就全给"会让未知 runtime 拿到它承载不了的 server。
//   - `allowIDs`：白名单。**nil = 不限制**（全量），**空但非 nil = 一个都不要**——这两者必须
//     区分开（`[]` 是用户显式选择"本会话不要任何 MCP"），故参数类型是 `*[]string` 而不是
//     `[]string`（后者无法表达"显式空"）。
func SelectMcpServers(all []McpServer, transports []string, allowIDs *[]string) []McpServer {
	want := make(map[string]bool, len(transports))
	for _, t := range transports {
		want[t] = true
	}
	var allow map[string]bool
	if allowIDs != nil {
		allow = make(map[string]bool, len(*allowIDs))
		for _, id := range *allowIDs {
			allow[id] = true
		}
	}
	out := make([]McpServer, 0, len(all))
	for _, m := range all {
		if !m.IsEnabled() {
			continue
		}
		if !want[m.Transport] {
			continue
		}
		if allow != nil && !allow[m.ID] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// RemoteMcpTransports 需要平台侧网关代连的传输（stdio 之外的全部）。
//
// stdio **不在**此列：它的子进程归底座自建自管（C-5），平台收进来是另一案（设计 §10.6-④ 的 S2）。
// 网关若把 stdio server 也暴露一遍，底座就会拿到两份同名工具。
func RemoteMcpTransports() []string {
	return []string{TransportSSE, TransportHTTP, TransportStreamableHTTP}
}

// IsRemoteTransport 报告某传输是否需要平台侧网关代连。
func IsRemoteTransport(transport string) bool {
	switch transport {
	case TransportSSE, TransportHTTP, TransportStreamableHTTP:
		return true
	default:
		return false
	}
}
