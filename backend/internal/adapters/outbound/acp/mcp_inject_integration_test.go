package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// TestMcpServersInjectionOverWire P2 注入端到端（真 stdio 往返）：client.NewSession 携带的
// 平台 MCP 子集 → NDJSON wire → agent 侧解码为判别联合（各 transport 子对象 + 有序 headers）。
// 空列表 → agent 侧得空数组（非 null；echo 等底座校验 mcpServers 必为数组）。
func TestMcpServersInjectionOverWire(t *testing.T) {
	agent := &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil)}
	cl := dialFake(t, agent, ClientOptions{})

	sess, err := cl.NewSession(context.Background(), SessionOptions{
		Cwd: "/work",
		McpServers: []McpServer{
			{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@fs/mcp"}},
			{Name: "evt", Transport: "sse", URL: "http://h/evt", Headers: map[string]string{"b": "2", "a": "1"}},
			{Name: "web", Transport: "http", URL: "http://h/rpc"},
			{Name: "web2", Transport: "streamable_http", URL: "http://h/rpc2"},
			{Name: "drop", Transport: "weird"}, // unknown → 不下发
			{Transport: "stdio"},               // 缺名 → 不下发
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID() == "" {
		t.Fatal("session id 为空")
	}

	agent.mu.Lock()
	got := agent.newCalls
	agent.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("agent 应收到 1 次 NewSession，got %d", len(got))
	}
	ms := got[0].McpServers
	if len(ms) != 4 {
		t.Fatalf("agent 应解码 4 个 server，got %d: %+v", len(ms), ms)
	}
	if s := ms[0].Stdio; s == nil || s.Name != "fs" || s.Command != "npx" || len(s.Args) != 2 || s.Env == nil {
		t.Fatalf("stdio 子对象不符: %+v", ms[0])
	}
	if e := ms[1].Sse; e == nil || e.Name != "evt" || e.Type != "sse" || e.Url != "http://h/evt" ||
		len(e.Headers) != 2 || e.Headers[0].Name != "a" || e.Headers[1].Name != "b" {
		t.Fatalf("sse 子对象/有序 headers 不符: %+v", ms[1])
	}
	if h := ms[2].Http; h == nil || h.Name != "web" || h.Type != "http" || h.Url != "http://h/rpc" {
		t.Fatalf("http 子对象不符: %+v", ms[2])
	}
	if ms[3].Http == nil || ms[3].Http.Name != "web2" {
		t.Fatalf("streamable_http 应归一 http 子对象: %+v", ms[3])
	}

	// 空列表：仍下发空数组（agent 校验 mcpServers 非 null）。
	if _, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work2"}); err != nil {
		t.Fatalf("无注入 NewSession: %v", err)
	}
	agent.mu.Lock()
	empty := agent.newCalls[1].McpServers
	agent.mu.Unlock()
	if empty == nil || len(empty) != 0 {
		t.Fatalf("无注入应得空数组，got %+v", empty)
	}
}
