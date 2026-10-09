package capability

// ——— 网关暴露的工具命名（M7-2 落地，M7-4 补策略） ———
//
// 网关把多台远端 server 的工具面拼成**一个**工具面交给底座，工具名因此成了平台侧责任：
// 底座只看见 `mate-gateway` 一台 server，模型看到的名字里必须能分辨来源，否则"两个 server
// 都有 search 工具"就会表现成"调了 search，但不是你以为的那台"。
//
// 命名形态：`<serverID>__<toolName>`（双下划线）。选它的理由：底座/模型侧无需任何额外映射，
// 名字自解释；且**注入端与网关端、探测面与真实面共用这一个函数**——M7-4 的对齐验收
// （probe 与 tools/list 同源）就是靠"只有一份实现"。
//
// 已知边界（M7-4 处理）：server id 与工具名都允许下划线，理论上可出现
// `"a" + "__" + "b__c" == "a__b" + "__" + "c"` 这种跨 server 的重名。故解析走
// `SplitGatewayToolName` 的**最长 id 前缀**而不是简单的 `strings.Cut`：它把歧义收敛成
// 唯一解（更长的那台 server 优先），M7-4 再按 fail-closed 策略在列举期就把这种组合拒掉。

// GatewayToolSep 网关工具名的来源分隔符。
const GatewayToolSep = "__"

// GatewayToolName 拼网关暴露的工具名（唯一实现；探测面与真实面必须逐字相同）。
func GatewayToolName(serverID, toolName string) string {
	return serverID + GatewayToolSep + toolName
}

// NamespacedTool 一条"要加来源前缀"的工具（`Name` 由 NamespaceTools 填，调用方不必自己拼）。
type NamespacedTool struct {
	ServerID string
	Raw      string // server 自己报的原名
	Name     string // 暴露名（含前缀）；由 NamespaceTools 统一填
}

// ToolNameConflict 一组互相冲突的暴露名（同一个名字被多台 server 占用）。
type ToolNameConflict struct {
	Name   string
	Claims []NamespacedTool
}

// NamespaceTools 批量加前缀并做 **fail-closed** 冲突裁决，返回**保留下来的下标**（调用方自己
// 保留原始载荷，例如工具描述/schema）与冲突明细。
//
// 为什么要冲突裁决：`SplitGatewayToolName` 走"最长 server id 前缀"，这只在**没有重名**时给出
// 唯一解。`serverID` 与工具名都允许下划线，所以 `"a" + "__" + "b__c"` 与 `"a__b" + "__" + "c"`
// 会撞成同一个暴露名，此时按名字反查来源是**歧义**的。
//
// 裁决规则是"**整组丢弃**"而不是"保留第一个"：保留第一个会让调用静默落到一台**不是调用方
// 以为的** server 上（安全性问题），而"这个工具不见了"是看得见的降级——后者可排查、前者不可。
// 冲突必须由调用方响亮留痕（日志 + 面板），否则用户只会看到"有些工具没了"。
func NamespaceTools(items []NamespacedTool) (keptIdx []int, conflicts []ToolNameConflict) {
	byName := make(map[string][]int, len(items))
	order := make([]string, 0, len(items))
	for i, it := range items {
		if it.ServerID == "" || it.Raw == "" {
			continue // 无名条目无法被调用，不入面（与探测面口径一致）
		}
		name := GatewayToolName(it.ServerID, it.Raw)
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		items[i].Name = name
		byName[name] = append(byName[name], i)
	}
	for _, name := range order {
		idx := byName[name]
		if len(idx) == 1 {
			keptIdx = append(keptIdx, idx[0])
			continue
		}
		claims := make([]NamespacedTool, 0, len(idx))
		for _, i := range idx {
			claims = append(claims, items[i])
		}
		conflicts = append(conflicts, ToolNameConflict{Name: name, Claims: claims})
	}
	return keptIdx, conflicts
}

// SplitGatewayToolName 从网关工具名解出来源 server 与原始工具名。
//
// `serverIDs` 是**当下**该 scope 的 server 集合（不是定义全量）：解析结果必须落在真实存在的
// 那几台上，否则"给一个已停用 server 的工具名"会被路由到不存在的目标。多台匹配时取**最长 id**
// （见文件头注释），无匹配 → ok=false（调用方按"未知工具"处理）。
func SplitGatewayToolName(name string, serverIDs []string) (serverID, toolName string, ok bool) {
	for _, id := range serverIDs {
		if id == "" {
			continue
		}
		prefix := id + GatewayToolSep
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		if len(id) > len(serverID) {
			serverID, toolName = id, name[len(prefix):]
		}
	}
	if serverID == "" {
		return "", "", false
	}
	return serverID, toolName, true
}
