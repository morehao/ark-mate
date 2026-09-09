package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// WSHandler 是实时事件通道（WS）的协议薄适配层（M0 基本版，无鉴权）：
// 订阅 eventbus → 按 attach 的聚合（conversation id）推送 event/snapshot 帧；
// 客户端帧 action=attach/detach/ping；服务端帧 type=snapshot/event/detached/pong。
// 传输层使用 github.com/coder/websocket（context 化 API：Read/Write/Ping 带 ctx，
// 控制帧（ping/pong/close）由库自动应答，Ping 需与 Reader 并发执行，见其 conn.go 注释）。
// 契约见 docs/design-and-roadmap.md §4.5。
type WSHandler struct {
	bus    *eventbus.Bus
	logger *slog.Logger
}

// NewWSHandler 构造 WSHandler。
func NewWSHandler(bus *eventbus.Bus) *WSHandler {
	return &WSHandler{bus: bus, logger: slog.Default()}
}

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second // 协议层 ping 的 pong 等待上限（探测死连接）
	wsPingPeriod     = 30 * time.Second // 协议层心跳间隔（防代理空闲断连）
	wsMaxMessageSize = 4096
)

// wsClientFrame 客户端 → 服务端帧。
type wsClientFrame struct {
	Action   string `json:"action"`              // attach | detach | ping
	AggID    string `json:"agg_id"`              // attach/detach 目标聚合（conversation id）
	SinceSeq *int64 `json:"since_seq,omitempty"` // attach 续传游标（省略→snapshot；数字含 0→重放）
}

// wsServerFrame 服务端 → 客户端帧。
type wsServerFrame struct {
	Type     string          `json:"type"`                // snapshot | event | gap | detached | pong
	AggID    string          `json:"agg_id,omitempty"`    // 归属聚合
	EventSeq int64           `json:"event_seq,omitempty"` // snapshot 水位 / event 的 seq / gap 的首丢 seq
	FromSeq  int64           `json:"from_seq,omitempty"`  // gap：重挂起点（= 首丢 seq）
	Event    *eventbus.Event `json:"event,omitempty"`     // event 帧载荷（type/seq/agg_id/version/payload）
	Reason   string          `json:"reason,omitempty"`    // detached 原因
	State    json.RawMessage `json:"state,omitempty"`     // snapshot 终态视图（M0 空对象）
}

// connState 单条 WS 连接状态。
type connState struct {
	conn *websocket.Conn

	ctx    context.Context // 连接生命周期 ctx（shutdown 时取消，解除 Read/Write/Ping 阻塞）
	cancel context.CancelFunc

	write chan wsServerFrame // 单写者队列（写帧必须经 writeLoop）

	mu       sync.Mutex
	attached map[string]bool

	closed    chan struct{}
	closeOnce sync.Once
}

// Handle 处理一次 WS 升级与生命周期。
func (h *WSHandler) Handle(c *gin.Context) {
	// M0 内网直连无鉴权：放行任意 Origin；M3 起改 OriginPatterns + 子协议 token 校验。
	ws, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Debug("ws upgrade failed", "err", err)
		return
	}
	defer ws.CloseNow()

	st := &connState{
		conn:     ws,
		write:    make(chan wsServerFrame, 256),
		attached: make(map[string]bool),
		closed:   make(chan struct{}),
	}
	st.ctx, st.cancel = context.WithCancel(context.Background())
	defer h.shutdown(st)

	ws.SetReadLimit(wsMaxMessageSize)

	// live 订阅以当前水位起步（attach 前的历史走 EventsAfter 重放，避免与 live 重叠）。
	live := h.bus.Subscribe(h.bus.LastSeq())
	defer h.bus.Unsubscribe(live)

	go h.writeLoop(st)
	go h.pingLoop(st)
	go h.livePump(st, live)

	h.readLoop(st)
}

// readLoop 读取客户端帧并执行 attach/detach/ping；出错即结束整条连接。
// coder/websocket 会在 Read 内自动应答协议层 ping/pong/close 控制帧，
// 此处只处理应用层 JSON 帧（二进制帧忽略）。
func (h *WSHandler) readLoop(st *connState) {
	for {
		typ, data, err := st.conn.Read(st.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f wsClientFrame
		if err := json.Unmarshal(data, &f); err != nil {
			h.send(st, wsServerFrame{Type: "detached", Reason: "bad frame"})
			continue
		}
		switch f.Action {
		case "attach":
			h.attach(st, f)
		case "detach":
			h.detach(st, f.AggID)
		case "ping":
			h.send(st, wsServerFrame{Type: "pong"})
		default:
			h.send(st, wsServerFrame{Type: "detached", Reason: "unknown action"})
		}
	}
}

// attach 执行「决策 + 注册」：
//   - since_seq 省略（nil）→ snapshot（冷挂载：空 state + 水位）+ 后续 live；
//   - since_seq 提供（≥0）→ EventsAfter(since_seq) 全量/增量重放该聚合 + 后续 live
//     （UI 冷开启既有会话传 0 = 全量回放重建会话视图；断线重连传 lastSeq = 增量续传）。
//
// 简化说明：attach 的重放与 live 分属两条入队路径，极端并发下事件可能交错；
// M1 的 §4.5 冻结面（持锁原子决策 + 单写者重放）再收敛顺序。
func (h *WSHandler) attach(st *connState, f wsClientFrame) {
	if f.AggID == "" {
		h.send(st, wsServerFrame{Type: "detached", Reason: "agg_id required"})
		return
	}
	st.mu.Lock()
	if st.attached[f.AggID] {
		st.mu.Unlock()
		return // 幂等
	}
	st.attached[f.AggID] = true
	st.mu.Unlock()

	if f.SinceSeq == nil {
		// 冷挂载：整包终态视图（M0 无持久化 → 空 state + 当前水位，客户端以此初始化游标）。
		h.send(st, wsServerFrame{Type: "snapshot", AggID: f.AggID, EventSeq: h.bus.LastSeq(), State: json.RawMessage(`{}`)})
		return
	}
	since := *f.SinceSeq
	if since < 0 {
		since = 0
	}
	// 淘汰即快照回退（§5.2）：请求起点早于该聚合的缓冲淘汰水位 → 该区间已不可增量续传，
	// 回整包 snapshot（M0/M1 snapshot 为空终态 + 当前水位，客户端据此重建游标并清空视图）。
	if low := h.bus.EvictedThrough(f.AggID); low > 0 && since < low {
		h.send(st, wsServerFrame{Type: "snapshot", AggID: f.AggID, EventSeq: h.bus.LastSeq(), State: json.RawMessage(`{}`)})
		return
	}
	for _, evt := range h.bus.EventsAfter(since) {
		if evt.AggregID == f.AggID {
			h.send(st, wsServerFrame{Type: "event", AggID: f.AggID, EventSeq: evt.Seq, Event: &evt})
		}
	}
}

func (h *WSHandler) detach(st *connState, aggID string) {
	st.mu.Lock()
	delete(st.attached, aggID)
	st.mu.Unlock()
	h.send(st, wsServerFrame{Type: "detached", AggID: aggID, Reason: "detached"})
}

// livePump 把 bus live 增量按已 attach 聚合过滤后投递到写队列；
// 慢订阅丢帧（bus gap 提示）→ 转 gap 帧，客户端据此立即重挂该聚合（按水位重放续传）。
func (h *WSHandler) livePump(st *connState, live *eventbus.Subscription) {
	for {
		select {
		case <-st.closed:
			return
		case evt := <-live.C:
			st.mu.Lock()
			ok := st.attached[evt.AggregID]
			st.mu.Unlock()
			if ok {
				frame := wsServerFrame{Type: "event", AggID: evt.AggregID, EventSeq: evt.Seq, Event: &evt}
				select {
				case st.write <- frame:
				case <-st.closed:
					return
				}
			}
		case g := <-live.Gap():
			st.mu.Lock()
			ok := st.attached[g.AggregID]
			st.mu.Unlock()
			if ok {
				frame := wsServerFrame{Type: "gap", AggID: g.AggregID, EventSeq: g.Seq, FromSeq: g.Seq}
				select {
				case st.write <- frame:
				case <-st.closed:
					return
				}
			}
		}
	}
}

// writeLoop 单写者：串行写队列帧（coder/websocket 不允许并发写）。
func (h *WSHandler) writeLoop(st *connState) {
	for {
		select {
		case <-st.closed:
			return
		case frame := <-st.write:
			b, err := json.Marshal(frame)
			if err != nil {
				h.logger.Debug("ws marshal failed", "type", frame.Type, "err", err)
				continue
			}
			wctx, wcancel := context.WithTimeout(st.ctx, wsWriteWait)
			err = st.conn.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				h.shutdown(st)
				return
			}
		}
	}
}

// pingLoop 协议层心跳（防代理空闲断连 + 探测死连接）。
// coder/websocket 的 Ping 与 readLoop 的 Read 并发执行（库要求如此）：它发出 ping，
// 等待 Read 路径读到 pong 后返回；ctx 超时（wsPongWait）未收到 pong → 判定死连接收尾。
// 客户端 {action:ping} 是应用层帧，由 readLoop 回 {type:pong}，与此处无关。
func (h *WSHandler) pingLoop(st *connState) {
	t := time.NewTicker(wsPingPeriod)
	defer t.Stop()
	for {
		select {
		case <-st.closed:
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(st.ctx, wsPongWait)
			err := st.conn.Ping(pctx)
			pcancel()
			if err != nil {
				h.logger.Debug("ws ping failed, closing", "err", err)
				h.shutdown(st)
				return
			}
		}
	}
}

// send 非阻塞入队（队列满视为消费者过慢，丢帧由 attach 续传/快照兜底）。
func (h *WSHandler) send(st *connState, f wsServerFrame) {
	select {
	case st.write <- f:
	default:
		h.logger.Debug("ws write queue full, dropping frame", "type", f.Type)
	}
}

// shutdown 幂等关闭连接内务：广播 closed（停循环）+ 取消连接 ctx（解除 Read/Write/Ping 阻塞）。
func (h *WSHandler) shutdown(st *connState) {
	st.closeOnce.Do(func() {
		close(st.closed)
		st.cancel()
	})
}
