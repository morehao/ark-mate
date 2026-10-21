package acp

import (
	"testing"
)

// TestToAcpMcpServers 平台 MCP 注入 → ACP 判别联合：每 server 一个 transport 子对象；
// http/streamable_http 归一 http；headers 排序；unknown/缺名丢弃；nil → nil。
func TestToAcpMcpServers(t *testing.T) {
	list := []McpServer{
		{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@fs/mcp"},
			Env: map[string]string{"Z_VAR": "2", "A_VAR": "1"}},
		{Name: "evt", Transport: "sse", URL: "http://h/evt", Headers: map[string]string{"b": "2", "a": "1"}},
		{Name: "web", Transport: "http", URL: "http://h/rpc"},
		{Name: "web2", Transport: "streamable_http", URL: "http://h/rpc2"},
		{Name: "noargs", Transport: "stdio", Command: "npx"}, // 无 args：必须落成空数组而非 null
		{Name: "drop", Transport: "weird"},
		{Transport: "stdio"}, // 缺名 → 丢弃
	}
	out := toAcpMcpServers(list)
	if len(out) != 5 {
		t.Fatalf("期望 5 条有效注入（stdio×2/sse/http/streamable_http），got %d", len(out))
	}

	if s := out[0].Stdio; s == nil || s.Name != "fs" || s.Command != "npx" || len(s.Args) != 2 || s.Env == nil {
		t.Fatalf("stdio 子对象不符: %+v", out[0])
	}
	// §13.3 M1：env 落到 ACP 的 EnvVariable 列表，且按键排序（wire 字节可复现）。
	if s := out[0].Stdio; len(s.Env) != 2 || s.Env[0].Name != "A_VAR" || s.Env[0].Value != "1" ||
		s.Env[1].Name != "Z_VAR" || s.Env[1].Value != "2" {
		t.Fatalf("stdio env 应为有序 EnvVariable: %+v", s.Env)
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
	// 无 args 的 stdio：Args 必须是**空切片而非 nil**。nil 会被 SDK 序列化成 `"args": null`，
	// 某些底座的 zod 校验会因此拒掉整个 session/new（历史实测；对外只剩一句无上下文的 "Invalid params"）。
	if s := out[4].Stdio; s == nil || s.Args == nil || len(s.Args) != 0 {
		t.Fatalf("无 args 的 stdio 应落成空数组（非 nil）: %+v", out[4])
	}

	if h := toAcpMcpServers(nil); h == nil || len(h) != 0 {
		t.Fatalf("nil 列表应得空数组（agent 校验非 null），got %+v", h)
	}
	if h := toAcpMcpServers([]McpServer{}); h == nil || len(h) != 0 {
		t.Fatalf("空列表应得空数组（agent 校验非 null），got %+v", h)
	}
}

// TestToAcpEnv map → 有序 []EnvVariable；nil/空 → 空切片（ACP 要求数组而非 null）。
func TestToAcpEnv(t *testing.T) {
	if e := toAcpEnv(nil); e == nil || len(e) != 0 {
		t.Fatalf("nil map 应得空切片，got %+v", e)
	}
	e := toAcpEnv(map[string]string{"Z": "1", "A": "2"})
	if len(e) != 2 || e[0].Name != "A" || e[0].Value != "2" || e[1].Name != "Z" {
		t.Fatalf("env 应按键排序: %+v", e)
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
