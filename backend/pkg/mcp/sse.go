package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ——— sse 传输（MCP 2024-11-05 的 legacy HTTP+SSE 双向通道，B3） ———
//
// 与 http/streamable_http 的区别：**双向长连接**。
//  1. 客户端 `GET <url>`（Accept: text/event-stream）建立 SSE 流；
//  2. 服务端先发一条 `event: endpoint` + `data: <uri>`（相对或绝对）声明**发消息端点**；
//  3. 客户端把 JSON-RPC 请求 `POST` 到该端点（服务端回 202 Accepted）；
//  4. 响应/通知仍经第 1 步的 SSE 流以 `event: message` 回来。
//
// 实现要点：
//   - 读流在后台 goroutine 里持续解析 SSE 帧，响应按 id 匹配交给 call；
//   - `endpoint` 事件到达前 call/notify 会等待（受 ctx 约束，超时 → mcp_timeout）；
//   - 相对 endpoint 按 GET url 解析；服务端反向请求（ping）就地应答，其它方法回 method_not_found；
//   - close 关闭 GET 流（取消 goroutine）——POST 是无状态的，无需收尾。

type sseTransport struct {
	postURL string
	headers map[string]string
	client  *http.Client

	mu     sync.Mutex
	nextID int

	msgs      chan rpcMessage // SSE message 事件（已解析的 JSON-RPC 消息）
	ready     chan struct{}   // endpoint 就绪（只关闭一次）
	readyMu   sync.Mutex
	readyOK   bool
	cancel    context.CancelFunc
	body      io.Closer
	errMu     sync.Mutex
	streamErr error // 读流失败原因（供 call 报错）
}

// dialSSE 建立 legacy SSE 双向通道：先 GET 流并等 `endpoint` 事件（受 ctx 约束）。
func dialSSE(ctx context.Context, s Server) (transport, error) {
	if strings.TrimSpace(s.URL) == "" {
		return nil, errf("mcp_url_invalid", "sse server %q 缺少 url", s.ID)
	}
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, s.URL, nil)
	if err != nil {
		cancel()
		return nil, errf("mcp_url_invalid", "sse GET 请求构造失败：%v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, errf("mcp_sse_connect_failed", "sse GET 连接失败：%v", err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		cancel()
		return nil, errf("mcp_sse_connect_failed", "sse GET 返回 HTTP %d：%s", resp.StatusCode, lastLine(strings.TrimSpace(string(body))))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		_ = resp.Body.Close()
		cancel()
		return nil, errf("mcp_sse_connect_failed", "sse GET 的 Content-Type 不是 text/event-stream（got %q）", ct)
	}

	t := &sseTransport{
		headers: s.Headers,
		client:  client,
		msgs:    make(chan rpcMessage, 16),
		ready:   make(chan struct{}),
		cancel:  cancel,
		body:    resp.Body,
	}
	go t.readStream(resp.Body, s.URL)

	select {
	case <-t.ready:
		return t, nil
	case <-ctx.Done():
		t.close()
		return nil, errf("mcp_timeout", "sse 等待 endpoint 事件：%v", ctx.Err())
	case <-time.After(defaultSSEEndpointWait):
		t.close()
		return nil, errf("mcp_sse_connect_failed", "sse 连接 %s 后仍未收到 endpoint 事件", defaultSSEEndpointWait)
	}
}

// defaultSSEEndpointWait 等服务端宣告 POST 端点的上限（部分实现会立即发送）。
const defaultSSEEndpointWait = 15 * time.Second

// readStream 解析 SSE 帧：`event: endpoint` 记录 POST 端点，`event: message` 投递 JSON-RPC 消息。
func (t *sseTransport) readStream(body io.Reader, base string) {
	defer close(t.msgs)
	rd := bufio.NewReaderSize(body, 64<<10)
	event := ""
	var data []string
	for {
		line, err := rd.ReadString('\n')
		if line == "" && err != nil {
			t.setStreamErr(fmt.Errorf("SSE 流结束：%v", err))
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			t.dispatch(event, data, base)
			event, data = "", nil
		case strings.HasPrefix(trimmed, ":"):
			// 注释/心跳，忽略
		case strings.HasPrefix(trimmed, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		}
		if err != nil {
			t.setStreamErr(fmt.Errorf("SSE 读取失败：%v", err))
			return
		}
	}
}

func (t *sseTransport) dispatch(event string, data []string, base string) {
	if len(data) == 0 {
		return
	}
	payload := strings.Join(data, "\n")
	if event == "endpoint" {
		post := strings.TrimSpace(payload)
		if post == "" {
			t.setStreamErr(fmt.Errorf("endpoint 事件为空"))
			return
		}
		if u, err := url.Parse(post); err == nil && !u.IsAbs() {
			if b, berr := url.Parse(base); berr == nil {
				post = b.ResolveReference(u).String()
			}
		}
		t.mu.Lock()
		t.postURL = post
		t.mu.Unlock()
		t.markReady()
		return
	}
	// message（或未标 event 的默认事件）
	var msg rpcMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return // 非 JSON-RPC 帧：忽略（不臆造语义）
	}
	select {
	case t.msgs <- msg:
	default:
		// 缓冲满：丢弃最旧的未消费消息，保证读流不阻塞（探测/调用是串行的，正常不会满）。
		select {
		case <-t.msgs:
		default:
		}
		select {
		case t.msgs <- msg:
		default:
		}
	}
}

func (t *sseTransport) markReady() {
	t.readyMu.Lock()
	defer t.readyMu.Unlock()
	if !t.readyOK {
		t.readyOK = true
		close(t.ready)
	}
}

func (t *sseTransport) setStreamErr(err error) {
	t.errMu.Lock()
	if t.streamErr == nil {
		t.streamErr = err
	}
	t.errMu.Unlock()
	t.markReady() // 避免 dial 一直等 endpoint
}

func (t *sseTransport) streamError() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.streamErr
}

func (t *sseTransport) close() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.body != nil {
		_ = t.body.Close()
	}
}

func (t *sseTransport) call(ctx context.Context, method string, params any, out any) error {
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
	if err := t.post(ctx, msg); err != nil {
		return err
	}
	want := strconv.Itoa(id)
	for {
		select {
		case m, ok := <-t.msgs:
			if !ok {
				if err := t.streamError(); err != nil {
					return err
				}
				return fmt.Errorf("SSE 流已关闭（未收到 id=%s 的响应）", want)
			}
			if m.Method != "" && m.ID != nil {
				t.reply(ctx, *m.ID, m.Method)
				continue
			}
			if m.ID == nil || string(*m.ID) != want {
				continue // 通知或其它请求的响应：串行流程下丢弃
			}
			if m.Error != nil {
				return fmt.Errorf("JSON-RPC 错误：%s", m.Error.String())
			}
			if out == nil {
				return nil
			}
			if len(m.Result) == 0 {
				return fmt.Errorf("响应缺少 result")
			}
			return json.Unmarshal(m.Result, out)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *sseTransport) notify(ctx context.Context, method string, params any) error {
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = raw
	}
	return t.post(ctx, msg)
}

// post 把一条 JSON-RPC 消息 POST 到 endpoint 事件宣告的地址（服务端应答 202/2xx 即可）。
func (t *sseTransport) post(ctx context.Context, msg rpcMessage) error {
	postURL, err := t.awaitEndpoint(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE POST 失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("SSE POST 返回 HTTP %d：%s", resp.StatusCode, lastLine(strings.TrimSpace(string(body))))
	}
	return nil
}

// awaitEndpoint 等 endpoint 事件（dial 阶段通常已就绪；未就绪时按 ctx 等待）。
func (t *sseTransport) awaitEndpoint(ctx context.Context) (string, error) {
	t.mu.Lock()
	post := t.postURL
	t.mu.Unlock()
	if post != "" {
		return post, nil
	}
	select {
	case <-t.ready:
		t.mu.Lock()
		post = t.postURL
		t.mu.Unlock()
		if post == "" {
			if err := t.streamError(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("SSE 未收到 endpoint 事件")
		}
		return post, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// reply 应答服务端反向请求（仅 ping 有意义；其余回 method_not_found，避免服务端等待超时）。
func (t *sseTransport) reply(ctx context.Context, id json.RawMessage, method string) {
	if method == "ping" {
		_ = t.post(ctx, rpcMessage{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`{}`)})
		return
	}
	_ = t.post(ctx, rpcMessage{JSONRPC: "2.0", ID: &id, Error: &rpcError{Code: -32601, Message: "method not found: " + method}})
}
