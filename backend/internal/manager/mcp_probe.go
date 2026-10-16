package manager

// ——— 连接器（MCP）探测面（已清空：实现全在 `domain/mcp`）———
//
// 探测主体（预算、状态记账、`tools/list` 权威快照替换 Tool 目录、启动期自动探测）早在 P2a-14
// 就落进了 `domain/mcp.Plane`（`Probe` / `AutoProbe`）；P4a 正文第三十八刀把这里最后三条一行转发
// 与 `svccapability.CapabilitySvc` 的对应声明一起删掉：
//
//	`ProbeMcpServer`      → 控制器直接 `h.mcpPlane.Probe(ctx, id)`
//	`AutoProbeMcpServers` → `app.go` 直接 `a.svc.McpPlane().AutoProbe(ctx)`
//	`SetMcpProbeTimeout`  → **零调用点的死转发**，随本刀删除（域里的 `SetProbeTimeout` 仍在，
//	                        它由域自己的测试用）
//
// **状态也一起搬走了**：`Service.mcpMu` / `mcpProbes` / `mcpProbeTimeout` 三个字段已删，
// 改由平面自持一把只保护这三个字段的锁（与 `skill.Plane` / `domain/interaction` 同形）——
// 它们此前就与 `Service.mu` 无关，搬走顺带消掉一处"同一结构体上两把无关的锁"。
//
// 留这条说明是因为**函数删了、文件还在**：下一个人看到空文件才知道这里的实现去哪了，
// 而不是以为漏搬了（与 `skills_write.go` / `mcps_write.go` 同一处置）。
