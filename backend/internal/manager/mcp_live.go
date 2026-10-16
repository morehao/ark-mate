package manager

// ——— 连接器（MCP）实时面（已清空：实现全在 `domain/mcp`）———
//
// 实时面（工具调用、资源、提示词；以及"取条目 + 解析密钥成明文"的唯一入口 `LiveServer`）早在
// P2a-14 就落进了 `domain/mcp.Plane`；P4a 正文第三十九刀把这里最后六条一行转发与
// `svccapability.CapabilitySvc` 的对应声明一起删掉：
//
//	`CallMcpTool`      → 控制器直接 `h.mcpPlane.CallTool(ctx, id, tool, req)`
//	`ListMcpResources` → `h.mcpPlane.ListResources(ctx, id)`
//	`ReadMcpResource`  → `h.mcpPlane.ReadResource(ctx, id, uri)`
//	`ListMcpPrompts`   → `h.mcpPlane.ListPrompts(ctx, id)`
//	`GetMcpPrompt`     → `h.mcpPlane.GetPrompt(ctx, id, name, req)`
//	`mcpLiveServer`    → 跨面集成测试改调 `svc.McpPlane().LiveServer(id)`
//
// 最后一条值得单独记一句：它此前**唯一**的存在理由是给 `mcp_secret_test.go` 当入口，而那是个
// 包内测试（`package manager`）——它本来就能直接走 `svc.McpPlane()`。为"测试少写几个字"留一条
// 转发，正是第三条硬规矩点名的形状（`svccapability` 的同名声明也随之失去最后一个消费者）。
// 那条用例要验的性质一句话没变：**装配起来的那份 Service**（真注册表 + 真库 + 真凭据面）上，
// 明文与解析后的密钥一起进客户端输入——它钉的是 2026-10-09 实测过的跨面缺陷（"会话里能用、
// 探测必失败"）。
//
// 为什么实时面与密钥面（`domain/mcp.Secrets`）分两个类型：密钥解析是**纯函数式**的
// （引用 → 明文，无状态），实时面要拿注册表里的条目、要按平面自己的预算发请求。合成一个
// 类型会让"只想解析一条引用"的调用方（网关、写面体检）也拿到客户端能力。
//
// 留这条说明是因为**函数删了、文件还在**：下一个人看到空文件才知道这里的实现去哪了，
// 而不是以为漏搬了（与 `skills_write.go` / `mcps_write.go` / `mcp_probe.go` 同一处置）。
