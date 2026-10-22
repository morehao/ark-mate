package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/morehao/golib/glog"
)

// ——— 远程 ACP 传输的服务端半场（A5） ———
//
// 与客户端半场（remote.go）对称：把「一个 ACP server（peerInput/peerOutput 字节流）」
// 暴露成远程端点，供本机/跨机的 ark-mate（或任何 ACP 客户端）以 http|ws 接入。
//
// 用途：
//   - 远程 runtime：`.ark-mate/config/runtimes.json` 声明的 http|ws 端点即由本层服务端半场承载
//     （历史上的 `mate acp-serve` 已随 echo 演示底座一并删除，远程传输**功能不受影响**）；
//   - 单测/端到端测试以真实 SDK agent 侧验证客户端传输适配器。
//
// HTTP 形态对齐 MCP Streamable HTTP 的既有约定（本项目 internal/infra/protocol/mcp 同款）：
//   - POST 一条客户端消息 → 200 + SSE 流（该请求期间的通知与 request_permission 都在流上），
//     客户端→服务端的「响应/通知」按约定回 202 且不占响应流；
//   - GET 长流承载「没有在途请求时」的服务端消息；
//   - 首个响应下发 Acp-Session-Id，后续请求须回带；未知 id 回 404。

// RemoteServerOptions 远程 ACP 服务端选项。
type RemoteServerOptions struct {
	// Serve 把 ACP server 绑定到一对字节流：peerInput 是 agent 写给客户端的字节，
	// peerOutput 是 agent 读到的客户端字节（与 acpsdk.NewAgentSideConnection 同序）。
	Serve func(peerInput io.Writer, peerOutput io.Reader)
	// SessionIDPrefix 会话 id 前缀（默认 "acp"；便于日志辨认）。
	SessionIDPrefix string
}

// RemoteHandler 是 Streamable HTTP 形态的服务端（挂到 http.Handler 即可）。
type RemoteHandler struct {
	opts RemoteServerOptions

	mu       sync.Mutex
	sessions map[string]*remoteSession
	seq      int64
}

// NewRemoteHandler 构造远程 ACP HTTP handler。
func NewRemoteHandler(opts RemoteServerOptions) *RemoteHandler {
	return &RemoteHandler{opts: opts, sessions: map[string]*remoteSession{}}
}

// serverStream 一条响应流（POST 请求流或 GET 长流）。
type serverStream struct {
	ch  chan []byte
	get bool
}

// remoteSession 一个 ACP 会话（一条 agent 连接）。
type remoteSession struct {
	agentIn  *io.PipeWriter // 客户端消息 → agent
	agentOut *io.PipeReader // agent 消息 → 客户端

	mu         sync.Mutex
	streams    map[int]*serverStream
	pending    map[string]chan []byte
	nextStream int
	closed     bool
}

// ServeHTTP 处理 POST（投递一条消息）与 GET（长流）。
func (h *RemoteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.serveGET(w, r)
	case http.MethodPost:
		h.servePOST(w, r)
	case http.MethodDelete:
		// 会话终止（MCP 约定）：关闭 agent 连接。
		if s := h.lookup(r.Header.Get(remoteSessionHeader)); s != nil {
			s.close()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *RemoteHandler) serveGET(w http.ResponseWriter, r *http.Request) {
	s := h.lookup(r.Header.Get(remoteSessionHeader))
	if s == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set(remoteSessionHeader, r.Header.Get(remoteSessionHeader))
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	ch := make(chan []byte, 64)
	id := s.subscribe(ch, true)
	defer s.unsubscribe(id)
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			if !writeSSE(w, flusher, line) {
				return
			}
		}
	}
}

func (h *RemoteHandler) servePOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &env)
	reqID := strings.Trim(string(env.ID), `"`)

	// 会话解析：带 id 则取已有会话（未知 → 404，客户端应重建）；否则新建并下发 id。
	sessionID := r.Header.Get(remoteSessionHeader)
	var s *remoteSession
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s-%d-%d", firstNonEmpty(h.opts.SessionIDPrefix, "acp"), time.Now().UnixNano(), atomic.AddInt64(&h.seq, 1))
		s = h.newSession()
		h.mu.Lock()
		h.sessions[sessionID] = s
		h.mu.Unlock()
	} else {
		s = h.lookup(sessionID)
		if s == nil {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
	}
	// 客户端→服务端的响应/通知（无 method 或无 id）不占响应流，直接投递。
	if env.Method == "" || reqID == "" {
		if _, err := s.agentIn.Write(append(body, '\n')); err != nil {
			http.Error(w, "agent closed", http.StatusGone)
			return
		}
		w.Header().Set(remoteSessionHeader, sessionID)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// 请求：必须先登记「响应流 + pending」再投递给 agent，否则 agent 可能在登记前就
	// 产出响应（尤其 initialize/session/new 这类快路径），消息会因没有订阅者而丢失。
	ch := make(chan []byte, 64)
	streamID := s.subscribe(ch, false)
	defer s.unsubscribe(streamID)
	s.mu.Lock()
	s.pending[reqID] = ch
	s.mu.Unlock()
	if _, err := s.agentIn.Write(append(body, '\n')); err != nil {
		http.Error(w, "agent closed", http.StatusGone)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set(remoteSessionHeader, sessionID)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			if !writeSSE(w, flusher, line) {
				return
			}
			if isResponseFor(line, reqID) {
				return
			}
		}
	}
}

// isResponseFor 判断一条消息是否是对 reqID 的响应。
func isResponseFor(line []byte, reqID string) bool {
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return false
	}
	return strings.Trim(string(env.ID), `"`) == reqID
}

func writeSSE(w io.Writer, flusher http.Flusher, line []byte) bool {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
		return false
	}
	if flusher != nil {
		flusher.Flush()
	}
	return true
}

func (h *RemoteHandler) lookup(sessionID string) *remoteSession {
	if sessionID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[sessionID]
}

func (h *RemoteHandler) newSession() *remoteSession {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	s := &remoteSession{
		agentIn:  c2aW,
		agentOut: a2cR,
		streams:  map[int]*serverStream{},
		pending:  map[string]chan []byte{},
	}
	// Serve 约定：peerInput = agent 写向客户端，peerOutput = agent 读自客户端。
	h.opts.Serve(a2cW, c2aR)
	go s.route()
	return s
}

// route 读 agent 输出并按 id 精确投递，通知类消息投给在途请求流（无则在 GET 长流）。
func (s *remoteSession) route() {
	sc := bufio.NewScanner(s.agentOut)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var env struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &env)
		key := strings.Trim(string(env.ID), `"`)

		s.mu.Lock()
		if key != "" {
			if ch, ok := s.pending[key]; ok {
				delete(s.pending, key)
				s.mu.Unlock()
				select {
				case ch <- line:
				default:
				}
				continue
			}
		}
		post, get := make([]chan []byte, 0, 2), make([]chan []byte, 0, 2)
		for _, st := range s.streams {
			if st.get {
				get = append(get, st.ch)
			} else {
				post = append(post, st.ch)
			}
		}
		s.mu.Unlock()
		targets := post
		if len(targets) == 0 {
			targets = get
		}
		for _, ch := range targets {
			select {
			case ch <- line:
			default:
			}
		}
	}
	if sc.Err() != nil {
		// 读循环 goroutine，没有可用的请求 ctx。
		glog.Infow(context.Background(), "remote server: agent 输出结束", "err", sc.Err())
	}
	s.close()
}

func (s *remoteSession) subscribe(ch chan []byte, get bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextStream++
	s.streams[s.nextStream] = &serverStream{ch: ch, get: get}
	return s.nextStream
}

func (s *remoteSession) unsubscribe(id int) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func (s *remoteSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.agentIn.Close()
	_ = s.agentOut.Close()
}

// ——— WS 形态的服务端 ———

// ServeWSSession 在一条已建立的 WebSocket 上承载一个 ACP 会话（一条文本帧 = 一条消息）。
func ServeWSSession(ctx context.Context, conn *websocket.Conn, opts RemoteServerOptions) error {
	conn.SetReadLimit(16 << 20)
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	opts.Serve(a2cW, c2aR)

	errCh := make(chan error, 1)
	go func() { // agent → ws
		sc := bufio.NewScanner(a2cR)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			if err := conn.Write(ctx, websocket.MessageText, line); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- sc.Err()
	}()
	for { // ws → agent
		typ, data, err := conn.Read(ctx)
		if err != nil {
			_ = c2aW.CloseWithError(err)
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		if _, err := c2aW.Write(append(data, '\n')); err != nil {
			return err
		}
	}
}
