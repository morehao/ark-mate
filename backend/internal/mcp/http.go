package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ——— http / streamable_http 传输（POST JSON-RPC；响应为 JSON 或 SSE 流） ———
//
// streamable_http 规范：initialize 响应可能带 Mcp-Session-Id，后续请求须回带；服务端可
// 以 application/json 直接应答，也可以 text/event-stream 流式应答（data: <json>）。
// sse（legacy 双向通道）不在本实现范围（见 ListTools）。

type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	nextID    int
	sessionID string
}

func listToolsHTTP(ctx context.Context, s Server) (Result, error) {
	if s.URL == "" {
		return Result{}, errf("mcp_url_invalid", "http/streamable_http server %q 缺少 url", s.ID)
	}
	tr := &httpTransport{
		url:     s.URL,
		headers: s.Headers,
		// 传输层不设全局超时：由调用方 ctx 控制（探测总预算）。
		client: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}},
	}
	return probe(ctx, tr)
}

func (t *httpTransport) close() {}

func (t *httpTransport) call(ctx context.Context, method string, params any, out any) error {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()
	rawID := json.RawMessage(strconv.Itoa(id))
	msg := rpcMessage{JSONRPC: "2.0", ID: &rawID, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = raw
	}
	body, err := t.post(ctx, msg)
	if err != nil {
		return err
	}
	resp, err := pickResponse(body, strconv.Itoa(id))
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("JSON-RPC 错误：%s", resp.Error.String())
	}
	if out == nil {
		return nil
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("响应缺少 result")
	}
	return json.Unmarshal(resp.Result, out)
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = raw
	}
	_, err := t.post(ctx, msg)
	return err
}

// post 发送一条 JSON-RPC 消息并读回响应体（有界）。
func (t *httpTransport) post(ctx context.Context, msg rpcMessage) ([]byte, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if sid == "" {
		if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
			t.mu.Lock()
			t.sessionID = got
			t.mu.Unlock()
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, lastLine(strings.TrimSpace(string(body))))
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		return body, nil // SSE：交由 pickResponse 解析
	}
	return body, nil
}

// pickResponse 从响应体（单 JSON 或 SSE 流）里挑出 id 匹配的 JSON-RPC 响应。
func pickResponse(body []byte, wantID string) (rpcMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return rpcMessage{}, fmt.Errorf("空响应体")
	}
	if trimmed[0] == '{' {
		var msg rpcMessage
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			return rpcMessage{}, fmt.Errorf("响应非 JSON：%v", err)
		}
		return msg, nil
	}
	// SSE：按空行分事件块，逐块取 data: 行拼成 JSON。
	var last rpcMessage
	found := false
	for _, block := range strings.Split(string(body), "\n\n") {
		var data []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &msg); err != nil {
			continue
		}
		if msg.ID != nil && string(*msg.ID) == wantID {
			return msg, nil
		}
		last = msg
		found = true
	}
	if found {
		return last, nil // 有响应但 id 不匹配：交回上层按 error/result 判定
	}
	return rpcMessage{}, fmt.Errorf("SSE 流中未找到 JSON-RPC 响应")
}
