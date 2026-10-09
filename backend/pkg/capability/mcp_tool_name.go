package capability

// ——— 网关暴露的工具命名（M7-2 落地，M7-4 补策略） ———
//
// 网关把多台远端 server 的工具面拼成**一个**工具面交给底座，工具名因此成了平台侧责任：
// 底座只看见 `forge-gateway` 一台 server，模型看到的名字里必须能分辨来源，否则"两个 server
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
