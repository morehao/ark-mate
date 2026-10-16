package manager

// ——— MCP server 定义写面（已清空：实现全在 `domain/mcp`）———
//
// 写面主体（闸门顺序、载体 CAS、内存入表、未解析密钥体检）在 `domain/mcp.Plane` 的
// `WriteServer` / `DeleteServer` 里。
//
// 与 `skills_write.go` 同一刀（P4a 正文第三十七刀）：三条一行转发（`CreateMcpServer` /
// `UpdateMcpServer` / `DeleteMcpServer`）与接口声明一起删，控制器直接经 `h.mcpPlane` 调用
// （为此给控制器补了一格 `mcpPlane *mcp.Plane` 字段——它原先只在构造 `NewServers` 时用一次）。
