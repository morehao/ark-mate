package acl

import (
	"sort"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ——— MCP server 注入（design doc §5.3）：平台数据面（capability.McpServer）→ ACP wire ———
//
// SessionOptions.McpServers 携带平台注册表选出的 MCP 子集；本文件负责把 acl.McpServer
// （传输中性表示）转换为 ACP SDK 的判别联合（每 server 一个 transport 子对象：
// stdio/http/sse）。unknown transport 直接丢弃（防御：注册表校验已挡，转换层再兜一次）。

// McpServer 是传输中性的 MCP server 注入表示（上层由 capability.McpServer 映射而来；
// headers 可含凭证——仅走 ACP wire 下发，REST 不读回）。
type McpServer struct {
	Name      string
	Transport string // stdio | sse | http | streamable_http
	Command   string // stdio 时
	Args      []string
	// Env stdio 子进程环境变量（§13.3 M1：调用方已把密钥型绑定解析成明文；这里只负责下发）。
	Env     map[string]string
	URL     string // http/sse 时（streamable_http 亦归 http 子对象）
	Headers map[string]string
}

// toAcpMcpServers 把注入列表转换为 ACP SDK 判别联合。空/无 → 空数组（部分底座校验
// mcpServers 必须为数组而非 null；与改造前 `[]acpsdk.McpServer{}` 语义一致）。
func toAcpMcpServers(list []McpServer) []acpsdk.McpServer {
	if len(list) == 0 {
		return []acpsdk.McpServer{}
	}
	out := make([]acpsdk.McpServer, 0, len(list))
	for _, m := range list {
		if m.Name == "" {
			continue
		}
		headers := toAcpHeaders(m.Headers)
		switch m.Transport {
		case "stdio":
			// Args **恒为非 nil 切片**：SDK 结构体没有 omitempty，nil → `"args": null`，
			// 而某些底座的 zod 校验要求数组、收到 null 会直接拒掉整个 session/new
			// （历史实测：不带 args 的 MCP 定义 → 会话建不起来，对外只剩一句无上下文的
			// "Invalid params"，用户完全看不出是哪个字段的问题）。
			// 与上面 McpServers 恒为数组、下面 env/headers 恒为空切片是同一条规矩：
			// 传输中性的"没有"在这一层必须落成 wire 上的"空"，不能是 null。
			args := m.Args
			if args == nil {
				args = []string{}
			}
			out = append(out, acpsdk.McpServer{
				Stdio: &acpsdk.McpServerStdio{
					Name: m.Name, Command: m.Command, Args: args, Env: toAcpEnv(m.Env),
				},
			})
		case "sse":
			out = append(out, acpsdk.McpServer{
				Sse: &acpsdk.McpServerSseInline{
					Name: m.Name, Type: "sse", Url: m.URL, Headers: headers,
				},
			})
		case "http", "streamable_http":
			out = append(out, acpsdk.McpServer{
				Http: &acpsdk.McpServerHttpInline{
					Name: m.Name, Type: "http", Url: m.URL, Headers: headers,
				},
			})
		default:
			// unknown transport：防御性丢弃
		}
	}
	return out
}

// toAcpEnv map → []acpsdk.EnvVariable（排序保证确定性；nil → 空切片：ACP 要求数组而非 null）。
func toAcpEnv(env map[string]string) []acpsdk.EnvVariable {
	if len(env) == 0 {
		return []acpsdk.EnvVariable{}
	}
	names := make([]string, 0, len(env))
	for n := range env {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]acpsdk.EnvVariable, 0, len(names))
	for _, n := range names {
		out = append(out, acpsdk.EnvVariable{Name: n, Value: env[n]})
	}
	return out
}

// toAcpHeaders map → []acpsdk.HttpHeader（排序保证确定性；nil → 空切片）。
func toAcpHeaders(h map[string]string) []acpsdk.HttpHeader {
	if len(h) == 0 {
		return []acpsdk.HttpHeader{}
	}
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]acpsdk.HttpHeader, 0, len(names))
	for _, n := range names {
		out = append(out, acpsdk.HttpHeader{Name: n, Value: h[n]})
	}
	return out
}
