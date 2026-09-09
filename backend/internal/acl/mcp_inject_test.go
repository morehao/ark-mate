package acl

import (
	"testing"
)

// TestToAcpMcpServers 平台 MCP 注入 → ACP 判别联合：每 server 一个 transport 子对象；
// http/streamable_http 归一 http；headers 排序；unknown/缺名丢弃；nil → nil。
func TestToAcpMcpServers(t *testing.T) {
	list := []McpServer{
		{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@fs/mcp"}},
		{Name: "evt", Transport: "sse", URL: "http://h/evt", Headers: map[string]string{"b": "2", "a": "1"}},
		{Name: "web", Transport: "http", URL: "http://h/rpc"},
		{Name: "web2", Transport: "streamable_http", URL: "http://h/rpc2"},
		{Name: "drop", Transport: "weird"},
		{Transport: "stdio"}, // 缺名 → 丢弃
	}
	out := toAcpMcpServers(list)
	if len(out) != 4 {
		t.Fatalf("期望 4 条有效注入（stdio/sse/http/streamable_http），got %d", len(out))
	}

	if s := out[0].Stdio; s == nil || s.Name != "fs" || s.Command != "npx" || len(s.Args) != 2 || s.Env == nil {
		t.Fatalf("stdio 子对象不符: %+v", out[0])
	}
	if e := out[1].Sse; e == nil || e.Type != "sse" || e.Url != "http://h/evt" || len(e.Headers) != 2 ||
		e.Headers[0].Name != "a" || e.Headers[0].Value != "1" || e.Headers[1].Name != "b" {
		t.Fatalf("sse 子对象/headers 排序不符: %+v", out[1])
	}
	if h := out[2].Http; h == nil || h.Type != "http" || h.Url != "http://h/rpc" {
		t.Fatalf("http 子对象不符: %+v", out[2])
	}
	if out[3].Http == nil || out[3].Http.Url != "http://h/rpc2" {
		t.Fatalf("streamable_http 应归一 http 子对象: %+v", out[3])
	}

	if h := toAcpMcpServers(nil); h == nil || len(h) != 0 {
		t.Fatalf("nil 列表应得空数组（agent 校验非 null），got %+v", h)
	}
	if h := toAcpMcpServers([]McpServer{}); h == nil || len(h) != 0 {
		t.Fatalf("空列表应得空数组（agent 校验非 null），got %+v", h)
	}
}

// TestToAcpHeaders map → 有序 []HttpHeader；nil/空 → 空切片。
func TestToAcpHeaders(t *testing.T) {
	if h := toAcpHeaders(nil); h == nil || len(h) != 0 {
		t.Fatalf("nil map 应得空切片，got %+v", h)
	}
	h := toAcpHeaders(map[string]string{"z": "1", "a": "2"})
	if len(h) != 2 || h[0].Name != "a" || h[0].Value != "2" || h[1].Name != "z" {
		t.Fatalf("headers 应按键排序: %+v", h)
	}
}
