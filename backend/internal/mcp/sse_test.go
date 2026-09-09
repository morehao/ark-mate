package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseTestServer 最小 legacy SSE MCP 服务端（B3 测试用）：
//   - GET /sse → text/event-stream，先发 endpoint 事件（相对地址），再转发 message 事件；
//   - POST /messages → 解析 JSON-RPC，把响应投递到当前流的发送通道。
type sseTestServer struct {
	srv      *httptest.Server
	outbox   chan any
	gotCalls []string
}

func newSSETestServer(t *testing.T) *sseTestServer {
	t.Helper()
	s := &sseTestServer{outbox: make(chan any, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		// 相对 endpoint（客户端须按 GET url 解析）。
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=test\n\n")
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-s.outbox:
				body, _ := json.Marshal(msg)
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		var msg rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		s.gotCalls = append(s.gotCalls, msg.Method)
		var result map[string]any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion,
				"serverInfo": map[string]any{"name": "sse-helper", "version": "9.9"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "sse_tool", "description": "SSE 工具"}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "sse-called"}}}
		case "resources/list":
			result = map[string]any{"resources": []map[string]any{{"uri": "file:///sse.txt", "name": "sse.txt"}}}
		case "prompts/list":
			result = map[string]any{"prompts": []map[string]any{{"name": "sse_prompt"}}}
		}
		w.WriteHeader(http.StatusAccepted)
		if msg.Method == "notifications/initialized" || msg.ID == nil {
			return
		}
		// 反向请求：id 为字符串，验证 call 侧按 id 字符串匹配。
		id := json.RawMessage(*msg.ID)
		s.outbox <- map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sseTestServer) server() Server {
	return Server{ID: "sse", Transport: "sse", URL: s.srv.URL + "/sse"}
}

// TestSSEListTools 走完整 legacy 双向通道：GET 流拿 endpoint → POST 请求 → 响应经流回。
func TestSSEListTools(t *testing.T) {
	ts := newSSETestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := ListTools(ctx, ts.server())
	if err != nil {
		t.Fatalf("ListTools(sse): %v", err)
	}
	if res.ServerName != "sse-helper" || res.ServerVersion != "9.9" {
		t.Fatalf("握手自述不符: %+v", res)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "sse_tool" {
		t.Fatalf("工具列表不符: %+v", res.Tools)
	}
	if strings.Join(ts.gotCalls, ",") != "initialize,notifications/initialized,tools/list" {
		t.Fatalf("POST 方法序列不符: %v", ts.gotCalls)
	}
}

// TestSSECallToolAndOthers sse 传输下 tools/call、resources/list、prompts/list 均可用。
func TestSSECallToolAndOthers(t *testing.T) {
	ts := newSSETestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := CallTool(ctx, ts.server(), "sse_tool", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("CallTool(sse): %v", err)
	}
	if got.TextOf() != "sse-called" {
		t.Fatalf("调用结果不符: %+v", got)
	}
	res, err := ListResources(ctx, ts.server())
	if err != nil {
		t.Fatalf("ListResources(sse): %v", err)
	}
	if len(res.Resources) != 1 || res.Resources[0].URI != "file:///sse.txt" {
		t.Fatalf("资源列表不符: %+v", res)
	}
	prompts, err := ListPrompts(ctx, ts.server())
	if err != nil {
		t.Fatalf("ListPrompts(sse): %v", err)
	}
	if len(prompts) != 1 || prompts[0].Name != "sse_prompt" {
		t.Fatalf("提示词列表不符: %+v", prompts)
	}
}

// TestSSENoEndpointEvent 未收到 endpoint 事件 → 受 ctx 约束超时（mcp_timeout）。
func TestSSENoEndpointEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // 只建流，不发 endpoint
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "sse", Transport: "sse", URL: srv.URL})
	if CodeOf(err) != "mcp_timeout" {
		t.Fatalf("期望 mcp_timeout，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestSSENonEventStream GET 不是 text/event-stream → mcp_sse_connect_failed。
func TestSSENonEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "sse", Transport: "sse", URL: srv.URL})
	if CodeOf(err) != "mcp_sse_connect_failed" {
		t.Fatalf("期望 mcp_sse_connect_failed，got %v (code=%q)", err, CodeOf(err))
	}
}

// TestSSEHTTPError GET 返回 4xx → mcp_sse_connect_failed（带状态码）。
func TestSSEHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no sse here", http.StatusNotFound)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ListTools(ctx, Server{ID: "sse", Transport: "sse", URL: srv.URL})
	if CodeOf(err) != "mcp_sse_connect_failed" || !strings.Contains(err.Error(), "404") {
		t.Fatalf("期望 404 连接失败，got %v", err)
	}
}

// TestSSEMissingURL 缺 url → mcp_url_invalid。
func TestSSEMissingURL(t *testing.T) {
	_, err := ListTools(context.Background(), Server{ID: "sse", Transport: "sse"})
	if CodeOf(err) != "mcp_url_invalid" {
		t.Fatalf("期望 mcp_url_invalid，got %v", err)
	}
}
