package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain 复用测试二进制作为 stdio MCP 服务端（FORGE_MCP_HELPER=1 时进入 helper 循环）。
func TestMain(m *testing.M) {
	if os.Getenv("FORGE_MCP_HELPER") == "1" {
		runHelper()
		return
	}
	os.Exit(m.Run())
}

// runHelper 最小 MCP stdio 服务端：initialize / initialized / tools/list（含分页与故障注入）。
func runHelper() {
	mode := os.Getenv("FORGE_MCP_HELPER_MODE")
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	for in.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			continue
		}
		if mode == "bad_json" {
			fmt.Fprintln(os.Stdout, "this is not json")
			_ = out.Flush()
			continue
		}
		switch msg.Method {
		case "initialize":
			if mode == "init_error" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32000, "message": "helper rejects initialize"}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "helper", "version": "1.2.3"},
			}})
		case "resources/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"resources": []map[string]any{
					{"uri": "file:///tmp/a.txt", "name": "a.txt", "mimeType": "text/plain", "size": 3},
					{"uri": "", "name": "无名 uri 应被保留（不臆造过滤）"},
				},
			}})
		case "resources/templates/list":
			if mode == "no_templates" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32601, "message": "method not found"}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"resourceTemplates": []map[string]any{{"uriTemplate": "file:///{path}", "name": "file"}},
			}})
		case "resources/read":
			var p struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.URI == "file:///missing" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32002, "message": "resource not found"}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"contents": []map[string]any{
					{"uri": p.URI, "mimeType": "text/plain", "text": "hello " + p.URI},
					{"uri": p.URI, "mimeType": "application/octet-stream", "blob": "aGk="},
				},
			}})
		case "prompts/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"prompts": []map[string]any{
					{"name": "greet", "description": "打招呼", "arguments": []map[string]any{
						{"name": "who", "required": true}, {"name": "lang"},
					}},
					{"name": ""}, // 无名条目：不目录化，但列表原样保留
				},
			}})
		case "prompts/get":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name == "missing" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32602, "message": "unknown prompt " + p.Name}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"description": "问候模板",
				"messages": []map[string]any{
					{"role": "user", "content": map[string]any{"type": "text", "text": "你好 " + p.Arguments["who"]}},
				},
			}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch mode {
			case "call_rpc_error":
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32602, "message": "unknown tool " + p.Name}})
			case "call_is_error":
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "工具执行失败：权限不足"}},
					"isError": true,
				}})
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "tool=" + p.Name},
						{"type": "text", "text": "args=" + string(p.Arguments)},
						{"type": "image", "data": "aGk=", "mimeType": "text/plain"},
					},
					"structuredContent": map[string]any{"ok": true},
				}})
			}
		case "tools/list":
			if mode == "no_response" {
				continue // 故意不回：测 ctx 超时
			}
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if mode == "paginate" && p.Cursor == "" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"tools":      []map[string]any{{"name": "page_one", "description": "第一页"}},
					"nextCursor": "c1",
				}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "read_file", "description": "读文件", "inputSchema": map[string]any{"type": "object"}},
					{"name": "write_file", "description": "写文件"},
					{"name": ""}, // 无名条目应被跳过
				},
			}})
		}
	}
}

func stdioServer(t *testing.T) Server {
	t.Helper()
	t.Setenv("FORGE_MCP_HELPER", "1")
	return Server{ID: "helper", Transport: "stdio", Command: os.Args[0]}
}

// TestStdioListToolsOK stdio 探测全流程：握手 → initialized → tools/list；空名工具跳过。
func TestStdioListToolsOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListTools(ctx, stdioServer(t))
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.ServerName != "helper" || res.ServerVersion != "1.2.3" {
		t.Fatalf("握手服务端自述不符: %+v", res)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "read_file" || res.Tools[1].Name != "write_file" {
		t.Fatalf("tools 不符: %+v", res.Tools)
	}
	if string(res.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("inputSchema 应原样保真，got %s", res.Tools[0].InputSchema)
	}
}

// TestStdioPagination tools/list nextCursor 翻页（两页合并）。
func TestStdioPagination(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "paginate")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListTools(ctx, stdioServer(t))
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 3 || res.Tools[0].Name != "page_one" || res.Tools[2].Name != "write_file" {
		t.Fatalf("分页合并不符: %+v", res.Tools)
	}
}

// TestStdioInitializeError 握手失败 → mcp_initialize_failed。
func TestStdioInitializeError(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "init_error")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := ListTools(ctx, stdioServer(t))
	if CodeOf(err) != "mcp_initialize_failed" {
		t.Fatalf("期望 mcp_initialize_failed，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestStdioBadJSON 响应非 JSON → tools 阶段报错（initialize 已过）。
func TestStdioBadJSON(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "bad_json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := ListTools(ctx, stdioServer(t))
	if CodeOf(err) != "mcp_initialize_failed" {
		t.Fatalf("期望 mcp_initialize_failed（首条响应即坏），got %v", err)
	}
}

// TestStdioTimeout ctx 到期 → mcp_timeout（子进程被 CommandContext 杀掉，读立即返回）。
func TestStdioTimeout(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "no_response")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := ListTools(ctx, stdioServer(t))
	if CodeOf(err) != "mcp_timeout" {
		t.Fatalf("期望 mcp_timeout，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestStdioSpawnFailed / TestStdioRelativeCommand 启动前置校验。
func TestStdioSpawnFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "x", Transport: "stdio", Command: "/nonexistent/mcp-server-xyz"})
	if CodeOf(err) != "mcp_spawn_failed" {
		t.Fatalf("期望 mcp_spawn_failed，got %v (code=%q)", err, CodeOf(err))
	}
}

func TestStdioRelativeCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "x", Transport: "stdio", Command: "npx"})
	if CodeOf(err) != "mcp_command_invalid" {
		t.Fatalf("期望 mcp_command_invalid，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestSSEUnsupported sse 传输明确降级（不臆造实现）。
func TestSSEUnsupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "x", Transport: "sse", URL: "http://127.0.0.1:1/sse"})
	if CodeOf(err) != "mcp_transport_unsupported" {
		t.Fatalf("期望 mcp_transport_unsupported，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestHTTPListTools streamable_http：initialize 带 Mcp-Session-Id → 后续请求回带；
// JSON 响应路径。
func TestHTTPListTools(t *testing.T) {
	var gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "http-helper", "version": "2.0"},
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			gotSession = r.Header.Get("Mcp-Session-Id")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"tools": []map[string]any{{"name": "http_tool", "description": "HTTP 工具"}},
			}})
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListTools(ctx, Server{ID: "h", Transport: "streamable_http", URL: srv.URL})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.ServerName != "http-helper" || len(res.Tools) != 1 || res.Tools[0].Name != "http_tool" {
		t.Fatalf("http 探测结果不符: %+v", res)
	}
	if gotSession != "sess-1" {
		t.Fatalf("后续请求应回带 Mcp-Session-Id，got %q", gotSession)
	}
}

// TestHTTPSSEResponse streamable_http 以 text/event-stream 应答（data: <json>）。
func TestHTTPSSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var result map[string]any
		if msg.Method == "initialize" {
			result = map[string]any{"protocolVersion": protocolVersion, "serverInfo": map[string]any{"name": "sse-helper"}}
		} else {
			result = map[string]any{"tools": []map[string]any{{"name": "sse_tool"}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListTools(ctx, Server{ID: "h", Transport: "http", URL: srv.URL})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.ServerName != "sse-helper" || len(res.Tools) != 1 || res.Tools[0].Name != "sse_tool" {
		t.Fatalf("SSE 响应解析不符: %+v", res)
	}
}

// TestHTTPErrorStatus 4xx/5xx → 报错（带状态码与正文尾）。
func TestHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "h", Transport: "http", URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("期望 HTTP 500 报错，got %v", err)
	}
	if CodeOf(err) != "mcp_initialize_failed" {
		t.Fatalf("期望 mcp_initialize_failed，got code=%q", CodeOf(err))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ——— B1：tools/call ———

// TestCallToolStdioOK 调用工具全流程：文本块拼接、非文本块跳过、structuredContent 保真。
func TestCallToolStdioOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := CallTool(ctx, stdioServer(t), "read_file", json.RawMessage(`{"path":"/tmp/a"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatal("不应是工具级错误")
	}
	if res.ServerName != "helper" || res.ServerVersion != "1.2.3" {
		t.Fatalf("握手自述不符: %+v", res)
	}
	if len(res.Content) != 3 {
		t.Fatalf("content 应原样保真 3 块: %+v", res.Content)
	}
	want := "tool=read_file\nargs={\"path\":\"/tmp/a\"}"
	if got := res.TextOf(); got != want {
		t.Fatalf("文本块拼接不符:\n got %q\nwant %q", got, want)
	}
	if string(res.StructuredContent) != `{"ok":true}` {
		t.Fatalf("structuredContent 应原样保真: %s", res.StructuredContent)
	}
}

// TestCallToolIsError 工具级失败（isError=true）不是传输/协议错误，须原样透传。
func TestCallToolIsError(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "call_is_error")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := CallTool(ctx, stdioServer(t), "read_file", nil)
	if err != nil {
		t.Fatalf("CallTool 不应报错（工具级失败经 isError 表达）: %v", err)
	}
	if !res.IsError || !strings.Contains(res.TextOf(), "权限不足") {
		t.Fatalf("isError/文本不符: %+v", res)
	}
}

// TestCallToolRPCError JSON-RPC 错误 → mcp_tools_call_failed（带稳定 code）。
func TestCallToolRPCError(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "call_rpc_error")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := CallTool(ctx, stdioServer(t), "no_such_tool", nil)
	if CodeOf(err) != "mcp_tools_call_failed" {
		t.Fatalf("期望 mcp_tools_call_failed，got %v (code=%q)", err, CodeOf(err))
	}
	if !strings.Contains(err.Error(), "unknown tool no_such_tool") {
		t.Fatalf("错误应带服务端原因: %v", err)
	}
}

// TestCallToolNameRequired 空工具名在本地即拒绝（不发请求）。
func TestCallToolNameRequired(t *testing.T) {
	_, err := CallTool(context.Background(), stdioServer(t), "  ", nil)
	if CodeOf(err) != "mcp_tool_name_required" {
		t.Fatalf("期望 mcp_tool_name_required，got %v", err)
	}
}

// TestCallToolHTTP http/streamable_http 传输同样支持 tools/call。
func TestCallToolHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "http-call", "version": "3.0"},
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "http-tool-ok"}},
			}})
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := CallTool(ctx, Server{ID: "http-call", Transport: "streamable_http", URL: srv.URL}, "t", nil)
	if err != nil {
		t.Fatalf("CallTool(http): %v", err)
	}
	if res.TextOf() != "http-tool-ok" || res.ServerName != "http-call" {
		t.Fatalf("结果不符: %+v", res)
	}
}

// TestCallToolSSEUnsupported sse 传输仍明确不支持（不臆造）。
func TestCallToolSSEUnsupported(t *testing.T) {
	_, err := CallTool(context.Background(), Server{ID: "s", Transport: "sse", URL: "http://127.0.0.1:1"}, "t", nil)
	if CodeOf(err) != "mcp_transport_unsupported" {
		t.Fatalf("期望 mcp_transport_unsupported，got %v", err)
	}
}

// TestCallToolArgsNotObject arguments 非对象在本地拒绝（MCP 规定 arguments 为对象）。
func TestCallToolArgsNotObject(t *testing.T) {
	_, err := CallTool(context.Background(), stdioServer(t), "echo", json.RawMessage(`"not-an-object"`))
	if CodeOf(err) != "mcp_tool_args_invalid" {
		t.Fatalf("期望 mcp_tool_args_invalid，got %v (code=%q)", err, CodeOf(err))
	}
	// null 合法（= 无参数）。
	if _, err := CallTool(context.Background(), stdioServer(t), "echo", json.RawMessage(`null`)); err != nil {
		t.Fatalf("null 应等价无参数: %v", err)
	}
}

// ——— B2：resources / prompts ———

// TestListResourcesAndTemplates resources/list + resources/templates/list（uri 原样保真）。
func TestListResourcesAndTemplates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListResources(ctx, stdioServer(t))
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if res.ServerName != "helper" || len(res.Resources) != 2 {
		t.Fatalf("资源列表不符: %+v", res)
	}
	if res.Resources[0].URI != "file:///tmp/a.txt" || res.Resources[0].MimeType != "text/plain" {
		t.Fatalf("资源条目不符: %+v", res.Resources[0])
	}
	if res.Resources[0].Size == nil || *res.Resources[0].Size != 3 {
		t.Fatalf("size 应保真: %+v", res.Resources[0])
	}
	if len(res.Templates) != 1 || res.Templates[0].URITemplate != "file:///{path}" || res.TemplatesError != "" {
		t.Fatalf("模板不符: %+v", res)
	}
}

// TestListResourcesTemplatesUnsupported 服务端未实现 templates → 降级为空 + 记录原因（不报错）。
func TestListResourcesTemplatesUnsupported(t *testing.T) {
	t.Setenv("FORGE_MCP_HELPER_MODE", "no_templates")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ListResources(ctx, stdioServer(t))
	if err != nil {
		t.Fatalf("模板不支持不应使整体失败: %v", err)
	}
	if len(res.Resources) != 2 || len(res.Templates) != 0 || res.TemplatesError == "" {
		t.Fatalf("降级结果不符: %+v", res)
	}
}

// TestReadResource text 与 blob 两种内容块原样保真。
func TestReadResource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := ReadResource(ctx, stdioServer(t), "file:///tmp/a.txt")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 2 {
		t.Fatalf("contents 不符: %+v", res.Contents)
	}
	if res.Contents[0].Text != "hello file:///tmp/a.txt" || res.Contents[1].Blob != "aGk=" {
		t.Fatalf("内容不符: %+v", res.Contents)
	}
	// 空 uri 本地拒绝。
	if _, err := ReadResource(ctx, stdioServer(t), "  "); CodeOf(err) != "mcp_resource_uri_required" {
		t.Fatalf("空 uri 应本地拒绝，got %v", err)
	}
	// 服务端错误 → mcp_resource_read_failed。
	if _, err := ReadResource(ctx, stdioServer(t), "file:///missing"); CodeOf(err) != "mcp_resource_read_failed" {
		t.Fatalf("期望 mcp_resource_read_failed，got %v", err)
	}
}

// TestListPromptsAndGet prompts/list 原样保留（含无名条目）+ prompts/get 参数透传。
func TestListPromptsAndGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prompts, err := ListPrompts(ctx, stdioServer(t))
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts) != 2 || prompts[0].Name != "greet" || len(prompts[0].Arguments) != 2 {
		t.Fatalf("提示词列表不符: %+v", prompts)
	}
	if !prompts[0].Arguments[0].Required || prompts[0].Arguments[1].Required {
		t.Fatalf("参数 required 不符: %+v", prompts[0].Arguments)
	}

	got, err := GetPrompt(ctx, stdioServer(t), "greet", map[string]string{"who": "世界"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if got.Description != "问候模板" || len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("prompts/get 结果不符: %+v", got)
	}
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(got.Messages[0].Content, &content); err != nil || content.Text != "你好 世界" {
		t.Fatalf("消息内容不符: %s (%v)", got.Messages[0].Content, err)
	}

	// 空名本地拒绝；服务端未知 → mcp_prompt_get_failed。
	if _, err := GetPrompt(ctx, stdioServer(t), "", nil); CodeOf(err) != "mcp_prompt_name_required" {
		t.Fatalf("空提示词名应本地拒绝，got %v", err)
	}
	if _, err := GetPrompt(ctx, stdioServer(t), "missing", nil); CodeOf(err) != "mcp_prompt_get_failed" {
		t.Fatalf("期望 mcp_prompt_get_failed，got %v", err)
	}
}
