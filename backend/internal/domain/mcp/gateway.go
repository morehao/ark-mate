package mcp

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 平台侧 MCP 网关复用的窄出口（M7-2）———
//
// 网关（`mate mcp bridge`）是**另一个进程**，但它必须与注入面共用同一套语义与同一份实现：
// "这个会话该看到哪些 MCP server"（选址）与"密钥引用怎么解析成明文"（凭据）。
// 网关重写一套的后果是**静默分叉**——用户停用的 server 仍然能被模型调用，或者同一份引用在
// 两个进程里解析出不同结果。故这两件事各开一个出口，而不是把整个平面暴露给网关。
//
// 刻意**不**导出"整个注入面"的口子：网关只该看见**远程**那一半（stdio 由底座直连；网关再
// 暴露一遍就是双份工具，模型会看到两个同名工具）。

// GatewayFace 返回网关应当暴露的**远程** server 定义（未解析密钥，顺序稳定）。
//
// 与注入面的两处**有意差异**：
//
//  1. **不按 runtime 的 transport 声明过滤**。这正是网关的收益（设计 §10.6-①）：底座只看见
//     一条 stdio 条目，"哪个底座支持 sse"这个问题对远程 server 不再成立。`runtimeID` 因此
//     不参与选址——它在网关侧只用于日志与 scope 校验。
//  2. **远程限定**：stdio server 走底座直连（C-5），不进网关的面。
//
// scope 的 runtime 与本进程构建期固定的那个**不一致** → **错误**（调用方降级为空面 + WARN）：
// 条目是平台拼的，对不上只可能是网关与核心不是同一份配置（或来自另一次构建）。此时**不能**
// 回落成"全量远程 server"——那是把授权判据反过来用了（对不上反而给得更多）。
//
// `mcpsPinned` 是会话钉住的白名单（nil = 该会话没钉 → 全量）：语义与注入面逐条一致，区别只在
// 上面两条。
func (p *Plane) GatewayFace(runtimeID string, mcpsPinned *[]string) ([]capability.McpServer, error) {
	if p.caps == nil {
		return nil, fmt.Errorf("能力注册表未初始化")
	}
	if runtimeID != runtime.SelectedRuntime() {
		return nil, fmt.Errorf("网关 scope 的 runtime %q 与本进程构建期固定的 %q 不一致（网关与核心不是同一份配置）",
			runtimeID, runtime.SelectedRuntime())
	}
	allowIDs := (*[]string)(nil)
	if mcpsPinned != nil {
		allowIDs = mcpsPinned
	}
	return capability.SelectMcpServers(p.caps.ListMcpServers(), capability.RemoteMcpTransports(), allowIDs), nil
}

// ResolveSecrets 把定义里的密钥引用解析成最终注入值（网关侧调用点）。
//
// 与注入/探测/实时面共用 `Secrets.Resolve` 一份实现（含"任一引用解析失败 → 全部失败原因"与
// "失败原因只含键名与引用、绝不含密钥值"两条纪律）。
func (p *Plane) ResolveSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	return p.secrets.Resolve(m)
}
