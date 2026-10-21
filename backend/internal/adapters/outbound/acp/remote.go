package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/morehao/golib/glog"
)

// ——— 远程 ACP 传输（A5）：把「一个远程 ACP server 端点」适配成 Transport ———
//
// 设计取舍：SDK 的 ClientSideConnection 只接受 io.Writer/io.Reader，所以远程传输不新增
// 连接抽象，而是**把网络连接伪装成一对流**：
//   - Stdin()  = 客户端写入的 NDJSON 消息流（按行切分后逐条投递到远端）
//   - Stdout() = 远端回来的 JSON-RPC 消息流（逐行写入管道）
// 这样 acp.Dial 及其握手/权限/elicitation 逻辑对 stdio 与远程完全同构，不需要第二套客户端。
//
// 两种形态（均由本文件实现，均无新依赖）：
//   - ws   ：一条 WebSocket 长连接，一条消息 = 一个文本帧（ACP 的 NDJSON 语义原样保留）。
//   - http ：Streamable HTTP（对齐 MCP 规范 / 本项目 internal/adapters/outbound/mcp 的既有做法）：
//     每条客户端消息一个 POST（Accept: application/json, text/event-stream）；
//     响应可以是单个 JSON，也可以是 SSE 流（`data: <json>`），后者承载同一请求期间的
//     session/update 通知与 session/request_permission 等**服务端发起**的消息；
//     另起一条 GET SSE 长流接收「没有在途请求时」的服务端消息（服务端不支持时降级为
//     仅在请求期间收消息，并记入诊断尾巴）；响应头带 `Acp-Session-Id`（兼容
//     `Mcp-Session-Id`）时后续请求回带。

// RemoteSpec 描述一个远程 ACP 端点。
type RemoteSpec struct {
	// Transport 传输形态："http" | "ws"（大小写不敏感；其余值报错）。
	Transport string
	// Endpoint 端点 URL：http(s):// 对应 http，ws(s):// 对应 ws。
	Endpoint string
	// Headers 附加请求头（鉴权等；WS 握手与 HTTP 请求都会带上）。
	Headers map[string]string
	// DialTimeout 建连超时（0 = 默认 30s；http 形态作用于请求头等待，ws 形态作用于握手）。
	DialTimeout time.Duration
}

const (
	// remoteSessionHeader ACP 会话标识响应头（与 MCP 的 Mcp-Session-Id 兼容）。
	remoteSessionHeader    = "Acp-Session-Id"
	remoteSessionHeaderAlt = "Mcp-Session-Id"
	// defaultRemoteDialTimeout 远程建连默认超时。
	defaultRemoteDialTimeout = 30 * time.Second
)

// NewRemoteTransport 构造远程传输（仅做参数校验，不建连；建连发生在 Start）。
func NewRemoteTransport(spec RemoteSpec) (Transport, error) {
	kind := strings.ToLower(strings.TrimSpace(spec.Transport))
	switch kind {
	case "http", "https", "streamable_http", "streamable-http":
		kind = "http"
	case "ws", "wss", "websocket":
		kind = "ws"
	default:
		return nil, NewError(ClassUnsupported, "unsupported_transport",
			fmt.Sprintf("不支持的远程传输 %q（支持 http|ws）", spec.Transport), nil)
	}
	u, err := url.Parse(strings.TrimSpace(spec.Endpoint))
	if err != nil || u.Host == "" {
		return nil, NewError(ClassRequest, "invalid_endpoint",
			fmt.Sprintf("远程端点 URL 非法：%q", spec.Endpoint), err)
	}
	switch kind {
	case "http":
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, NewError(ClassRequest, "endpoint_scheme_mismatch",
				fmt.Sprintf("http 传输的端点需为 http(s)://，得到 %q", spec.Endpoint), nil)
		}
	case "ws":
		if u.Scheme != "ws" && u.Scheme != "wss" {
			return nil, NewError(ClassRequest, "endpoint_scheme_mismatch",
				fmt.Sprintf("ws 传输的端点需为 ws(s)://，得到 %q", spec.Endpoint), nil)
		}
	}
	timeout := spec.DialTimeout
	if timeout <= 0 {
		timeout = defaultRemoteDialTimeout
	}
	return &remoteTransport{
		kind:       kind,
		endpoint:   u.String(),
		headers:    spec.Headers,
		dialTimout: timeout,
		outR:       nil,
	}, nil
}

// remoteTransport 见文件头注释。Stdin/Stdout 分别由一对 io.Pipe 承载。
type remoteTransport struct {
	kind       string
	endpoint   string
	headers    map[string]string
	dialTimout time.Duration

	startOnce sync.Once
	closeOnce sync.Once
	startErr  error
	closeErr  error

	ctx    context.Context
	cancel context.CancelFunc

	inR  *io.PipeReader // 客户端写入的消息（按行切分）
	inW  *io.PipeWriter
	outR *io.PipeReader // 远端消息（逐行）
	outW *io.PipeWriter

	mu        sync.Mutex
	tail      []string
	sessionID string

	// ws
	conn *websocket.Conn
	// http
	httpClient *http.Client

	wg sync.WaitGroup
}

var _ Transport = (*remoteTransport)(nil)

// Start 建立远端连接并启动收发协程。ctx 仅用于建连与后续读循环的生命周期。
func (t *remoteTransport) Start(ctx context.Context) error {
	t.startOnce.Do(func() {
		t.startErr = t.start(ctx)
	})
	return t.startErr
}

func (t *remoteTransport) start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(context.WithoutCancel(ctx))
	t.inR, t.inW = io.Pipe()
	t.outR, t.outW = io.Pipe()

	switch t.kind {
	case "ws":
		if err := t.startWS(t.ctx); err != nil {
			t.cancel()
			return err
		}
	case "http":
		t.httpClient = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: t.dialTimout}}
		// GET 长流（可选）：无在途请求时接收服务端消息；服务端不支持则降级并记录。
		t.wg.Add(1)
		go t.runHTTPGetStream(t.ctx)
	default:
		t.cancel()
		return NewError(ClassUnsupported, "unsupported_transport", "未知远程传输 "+t.kind, nil)
	}

	// 客户端写入 → 远端投递
	t.wg.Add(1)
	go t.runWriter(t.ctx)
	return nil
}

func (t *remoteTransport) startWS(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, t.dialTimout)
	defer cancel()
	h := http.Header{}
	for k, v := range t.headers {
		h.Set(k, v)
	}
	conn, _, err := websocket.Dial(dialCtx, t.endpoint, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.recordTail("ws dial failed: " + err.Error())
		return NewError(ClassConnection, "remote_dial_failed",
			"连接远程 ACP 端点失败（ws: "+t.endpoint+"）", err)
	}
	// 单条消息可能远大于默认 32KiB 读上限（长工具输出/大 diff），放宽到 16MiB。
	conn.SetReadLimit(16 << 20)
	t.conn = conn
	t.wg.Add(1)
	go t.runWSReader(ctx)
	return nil
}

func (t *remoteTransport) runWSReader(ctx context.Context) {
	defer t.wg.Done()
	defer func() { _ = t.outW.Close() }()
	for {
		typ, data, err := t.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !isNormalWSClose(err) {
				t.recordTail("ws read: " + err.Error())
				_ = t.outW.CloseWithError(err)
				return
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		if err := t.emit(data); err != nil {
			return
		}
	}
}

func (t *remoteTransport) runWriter(ctx context.Context) {
	defer t.wg.Done()
	sc := bufio.NewScanner(t.inR)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := t.deliver(ctx, line); err != nil {
			t.recordTail("deliver: " + err.Error())
			_ = t.outW.CloseWithError(err)
			return
		}
	}
}

// deliver 把一条客户端消息投递到远端。
func (t *remoteTransport) deliver(ctx context.Context, msg string) error {
	switch t.kind {
	case "ws":
		if t.conn == nil {
			return errors.New("ws 连接未建立")
		}
		return t.conn.Write(ctx, websocket.MessageText, []byte(msg))
	case "http":
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			t.postOne(ctx, msg)
		}()
		return nil
	default:
		return errors.New("未知传输 " + t.kind)
	}
}

// postOne 发送一条消息并把响应流（JSON 或 SSE）中的消息喂给 Stdout。
func (t *remoteTransport) postOne(ctx context.Context, msg string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, strings.NewReader(msg))
	if err != nil {
		t.recordTail("http request: " + err.Error())
		return
	}
	t.applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		t.recordTail("http post: " + err.Error())
		_ = t.outW.CloseWithError(err)
		return
	}
	defer resp.Body.Close()
	t.captureSessionID(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		line := fmt.Sprintf("http post %s: HTTP %d %s", t.endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
		t.recordTail(line)
		_ = t.outW.CloseWithError(errors.New(line))
		return
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		t.readSSE(ctx, resp.Body)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		t.recordTail("http read body: " + err.Error())
		return
	}
	for _, chunk := range splitMessages(body) {
		if err := t.emit(chunk); err != nil {
			return
		}
	}
}

// runHTTPGetStream 打开 GET SSE 长流（服务端不支持时降级：只记录诊断，不影响 POST 通道）。
func (t *remoteTransport) runHTTPGetStream(ctx context.Context) {
	defer t.wg.Done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint, nil)
	if err != nil {
		return
	}
	t.applyHeaders(req)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			t.recordTail("http get stream: " + err.Error())
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		t.recordTail(fmt.Sprintf("http get stream: HTTP %d（服务端无 GET 长流 → 仅请求期间接收服务端消息）", resp.StatusCode))
		return
	}
	t.readSSE(ctx, resp.Body)
}

// readSSE 解析 SSE 流：每个 data: 行的 JSON 作为一条消息。
func (t *remoteTransport) readSSE(ctx context.Context, body io.Reader) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") ||
			strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		payload := strings.TrimPrefix(line, "data:")
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if err := t.emit([]byte(payload)); err != nil {
			return
		}
	}
}

// applyHeaders 附加配置头 + 会话标识头。
func (t *remoteTransport) applyHeaders(req *http.Request) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		req.Header.Set(remoteSessionHeader, sid)
	}
}

// captureSessionID 记录服务端下发的会话标识（后续请求回带）。
func (t *remoteTransport) captureSessionID(resp *http.Response) {
	sid := resp.Header.Get(remoteSessionHeader)
	if sid == "" {
		sid = resp.Header.Get(remoteSessionHeaderAlt)
	}
	if sid == "" {
		return
	}
	t.mu.Lock()
	t.sessionID = sid
	t.mu.Unlock()
}

// emit 把一条远端消息写入 Stdout（逐行，与 NDJSON 语义一致）。
func (t *remoteTransport) emit(msg []byte) error {
	msg = []byte(strings.TrimSpace(string(msg)))
	if len(msg) == 0 {
		return nil
	}
	// 非 JSON 行（如服务端日志混入）不应毒化协议流：记诊断并跳过。
	if !json.Valid(msg) {
		t.recordTail("drop non-JSON message: " + truncate(string(msg), 200))
		return nil
	}
	if _, err := t.outW.Write(append(msg, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *remoteTransport) recordTail(line string) {
	t.mu.Lock()
	if len(t.tail) >= stderrTailCap {
		copy(t.tail, t.tail[1:])
		t.tail[len(t.tail)-1] = line
	} else {
		t.tail = append(t.tail, line)
	}
	t.mu.Unlock()
	// 行可能来自读循环 goroutine，没有可用的请求 ctx。
	glog.Infow(context.Background(), "remote transport", "kind", t.kind, "endpoint", t.endpoint, "line", line)
}

// Stdin 返回客户端写入端（Start 之后可用）。
func (t *remoteTransport) Stdin() io.Writer {
	if t.inW == nil {
		return nil
	}
	return t.inW
}

// Stdout 返回远端消息读取端（Start 之后可用）。
func (t *remoteTransport) Stdout() io.Reader {
	if t.outR == nil {
		return nil
	}
	return t.outR
}

// StderrTail 返回连接级诊断（建连失败、HTTP 状态、被丢弃的非 JSON 消息）。
func (t *remoteTransport) StderrTail() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.tail))
	copy(out, t.tail)
	return out
}

// SessionID 返回服务端下发的会话标识（未协商到则空；http 形态用）。
func (t *remoteTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// Close 断开远端连接并回收管道（幂等；不等待在途 POST 完成，靠 ctx 取消中止）。
func (t *remoteTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
		if t.conn != nil {
			_ = t.conn.Close(websocket.StatusNormalClosure, "client closing")
		}
		if t.inW != nil {
			_ = t.inW.Close()
		}
		if t.inR != nil {
			_ = t.inR.Close()
		}
		if t.outW != nil {
			_ = t.outW.Close()
		}
		done := make(chan struct{})
		go func() {
			t.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// 在途请求未及时退出：管道已关闭，协程会随 ctx 取消结束，不阻塞调用方。
		}
	})
	return t.closeErr
}

// ——— 小工具 ———

// splitMessages 把可能含多行 JSON（NDJSON）的响应体切成单条消息。
func splitMessages(body []byte) [][]byte {
	out := make([][]byte, 0, 1)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, []byte(line))
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isNormalWSClose 判断是否为对端正常关闭（不记为诊断错误）。
func isNormalWSClose(err error) bool {
	if err == nil {
		return true
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return true
	}
	return false
}
