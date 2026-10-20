package runtime

import "github.com/morehao/ark-mate/internal/infra/acp"

// ——— 执行面的**平台值类型**（C2 收尾：连接与生命周期）———
//
// 建会话的入参（MCP 注入、cwd）与"这条连接上还有哪些原生会话"都是编排侧必须表达的事实。
// 它们此前是 `acp.SessionOptions` / `acp.McpServer` / `acp.SessionInfo`，于是会话域为了
// 调一次 `NewSession` 就认识协议库。现在它们是平台值，协议形状只在适配器内部出现。

// McpServer 一条注入给底座的 MCP server（平台侧已解析好密钥；这里只负责下发）。
type McpServer struct {
	// Name 条目名（会话内的唯一标识）。
	Name string
	// Transport 传输形态：stdio | sse | http | streamable_http。
	Transport string
	// Command / Args / Env stdio 形态的启动面（Env 里的密钥型绑定已由调用方解析成明文）。
	Command string
	Args    []string
	Env     map[string]string
	// URL / Headers http(s)/sse 形态的端点与附加请求头。
	URL     string
	Headers map[string]string
}

// SessionRef 底座侧的一条**已有会话**（目录取样与"最近会话"候选的来源）。
type SessionRef struct {
	// ID 底座侧的原生会话 id（原文；平台侧要加 `rt_` 壳才落库）。
	ID string
	// Cwd 该会话的工作目录（候选枚举要求非空：空 cwd 无法恢复）。
	Cwd string
	// Title 底座给的标题（可空）。
	Title string
	// UpdatedAt 底座给的最近更新时刻文本（可空；顺序以底座返回的次序为准）。
	UpdatedAt string
}

// McpServersOfACP 把协议形状的注入列表折成平台值（适配器边界的唯一转换入口）。
func McpServersOfACP(in []acp.McpServer) []McpServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]McpServer, 0, len(in))
	for _, s := range in {
		out = append(out, McpServer{
			Name: s.Name, Transport: s.Transport, Command: s.Command,
			Args: append([]string(nil), s.Args...), Env: cloneStringMap(s.Env),
			URL: s.URL, Headers: cloneStringMap(s.Headers),
		})
	}
	return out
}

// McpServersToACP 是 `McpServersOfACP` 的逆（写面下发用；同样只在适配器里调）。
func McpServersToACP(in []McpServer) []acp.McpServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]acp.McpServer, 0, len(in))
	for _, s := range in {
		out = append(out, acp.McpServer{
			Name: s.Name, Transport: s.Transport, Command: s.Command,
			Args: append([]string(nil), s.Args...), Env: cloneStringMap(s.Env),
			URL: s.URL, Headers: cloneStringMap(s.Headers),
		})
	}
	return out
}

// SessionRefsOfACP 把协议的会话列表折成平台值。
func SessionRefsOfACP(in []acp.SessionInfo) []SessionRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]SessionRef, 0, len(in))
	for _, s := range in {
		out = append(out, SessionRef{ID: s.ID, Cwd: s.Cwd, Title: s.Title, UpdatedAt: s.UpdatedAt})
	}
	return out
}

// cloneStringMap 复制一份 map（nil 进 nil 出）：平台值与适配器不共享可变状态。
func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
