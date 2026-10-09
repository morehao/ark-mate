package manager

// ——— 平台侧 MCP 网关复用的窄出口（M7-2）———
//
// 网关（`forge mcp bridge`）是**另一个进程**，但它必须与注入面共用同一套语义与同一份实现：
// "这个 agent 该看到哪些 MCP server"（选址）与"密钥引用怎么解析成明文"（凭据）。
// 网关重写一套的后果是**静默分叉**——用户停用的 server 仍然能被模型调用，或者同一份引用在
// 两个进程里解析出不同结果。故这两件事各开一个出口，而不是把整个 Service 暴露给网关。
//
// 刻意**不**导出 `McpServersFor` 那种"整个注入面"的口子：网关只该看见**远程**那一半
// （stdio 由底座直连；网关再暴露一遍就是双份工具，模型会看到两个同名工具）。

import (
	"fmt"

	"github.com/morehao/ark-forge/pkg/capability"
)

// GatewayMcpFace 返回网关应当暴露的**远程** server 定义（未解析密钥，顺序稳定）。
//
// 与注入面的两处**有意差异**：
//
//  1. **不按 runtime 的 transport 声明过滤**。这正是网关的收益（设计 §10.6-①）：底座只看见
//     一条 stdio 条目，"哪个底座支持 sse"这个问题对远程 server 不再成立。`runtimeID` 因此
//     不参与选址——它在网关侧只用于日志与 scope 校验。
//  2. **远程限定**：stdio server 走底座直连（C-5），不进网关的面。
//
// 未知 agent → **错误**（调用方降级为空面 + WARN）：网关的 agent id 来自注入条目，查不到只能
// 说明条目与库不一致（agent 被删了）。此时**不能**回落成"全量远程 server"——那是把授权判据
// 反过来用了（查不到反而给得更多）。这条与注入面不同，因为注入面的 agent 一定来自会话行。
//
// `mcpsPinned` 是会话钉住的白名单（nil = 该会话没钉 → 回落 agent 声明）：语义与
// `mcpSelectionFor` 逐条一致，区别只在上面两条。
func (s *Service) GatewayMcpFace(agentID string, mcpsPinned *[]string) ([]capability.McpServer, error) {
	if s.caps == nil {
		return nil, fmt.Errorf("能力注册表未初始化")
	}
	agent, ok := s.AgentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q 不存在（注入条目与库不一致）", agentID)
	}
	allowIDs := (*[]string)(nil)
	if mcpsPinned != nil {
		allowIDs = mcpsPinned
	} else if agent != nil {
		allowIDs = agent.Mcps
	}
	return capability.SelectMcpServers(s.caps.ListMcpServers(), capability.RemoteMcpTransports(), allowIDs), nil
}

// ResolveMcpSecrets 把定义里的密钥引用解析成最终注入值（网关侧调用点）。
//
// 与注入面共用 `resolveMcpSecrets` 一份实现（含"任一引用解析失败 → 全部失败原因"与
// "失败原因只含键名与引用、绝不含密钥值"两条纪律）。
func (s *Service) ResolveMcpSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	return s.resolveMcpSecrets(m)
}
