package registry

import (
	"context"
	"encoding/json"

	"github.com/morehao/ark-mate/internal/core/protocol"
	dommcp "github.com/morehao/ark-mate/internal/domain/mcp"
	mcpclient "github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// ——— MCP 客户端实现体的适配（A2，2026-10-22）———
//
// 域的 `LiveClient` 端口只认 `protocol` 平台值；`infra/protocol/mcp` 的包级函数认的是本包自己的
// `Server` 与稳定 code。这一层就是把两边接起来的**唯一**地方——它住在装配面（L6），所以
// `domain` 不必 import `infra`（G2），`infra` 也不必认识域（adapter 禁入应用内核）。
//
// 为什么是结构体而不是把包级函数直接交给域：包级函数不是接口实现，端口要求**方法集**；
// 这层同时是"协议输入怎么摆"的唯一映射点（`Name` → `ID`，见下）。
type mcpLiveClient struct{}

// NewMcpLiveClient 返回注入给 `domain/mcp` 的客户端实现体（无状态，可反复传）。
//
// 导出是为了让不经 `NewPlanes` 的装配面（路由/用例的测试夹具）能用**同一个**实现，
// 而不是各写一份自己的桩——两份实现在"错误 code 词表"上漂移时，REST 分类会静默改档。
func NewMcpLiveClient() dommcp.LiveClient { return mcpLiveClient{} }

// 编译期钉住：端口一旦加方法，这行先红（比运行期空指针早）。
var _ dommcp.LiveClient = mcpLiveClient{}

// toClientServer 平台值 → 协议实现的探测输入。
//
// `protocol.McpServer.Name` 是定义 id（会话里显示的名字是另一回事），而协议实现拿它当
// **身份**（日志、池键、指纹）。两边同值不同名，映射写在一处，别在调用点各拼一次。
func toClientServer(s protocol.McpServer) mcpclient.Server {
	return mcpclient.Server{
		ID: s.Name, Transport: s.Transport, Command: s.Command,
		Args: s.Args, URL: s.URL, Headers: s.Headers, Env: s.Env,
	}
}

func (mcpLiveClient) ListTools(ctx context.Context, s protocol.McpServer) (protocol.McpToolList, error) {
	return mcpclient.ListTools(ctx, toClientServer(s))
}

func (mcpLiveClient) CallTool(ctx context.Context, s protocol.McpServer, name string, args json.RawMessage) (protocol.McpCallResult, error) {
	return mcpclient.CallTool(ctx, toClientServer(s), name, args)
}

func (mcpLiveClient) ListResources(ctx context.Context, s protocol.McpServer) (protocol.McpResourceList, error) {
	return mcpclient.ListResources(ctx, toClientServer(s))
}

func (mcpLiveClient) ReadResource(ctx context.Context, s protocol.McpServer, uri string) (protocol.McpReadResourceResult, error) {
	return mcpclient.ReadResource(ctx, toClientServer(s), uri)
}

func (mcpLiveClient) ListPrompts(ctx context.Context, s protocol.McpServer) ([]protocol.McpPrompt, error) {
	return mcpclient.ListPrompts(ctx, toClientServer(s))
}

func (mcpLiveClient) GetPrompt(ctx context.Context, s protocol.McpServer, name string, args map[string]string) (protocol.McpGetPromptResult, error) {
	return mcpclient.GetPrompt(ctx, toClientServer(s), name, args)
}

func (mcpLiveClient) CodeOf(err error) string { return mcpclient.CodeOf(err) }
