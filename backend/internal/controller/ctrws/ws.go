// Package ctrws 承载实时事件通道（WS）控制器。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrws

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/glog"
)

// WSCtr 实时事件通道（WS）控制器。
type WSCtr interface {
	Handle(c *gin.Context)
}

var _ WSCtr = (*wsCtr)(nil)

// defaultAttachDeadline 首帧 attach 的截止时限（真源在 credential.StreamAttachDeadline，
// 与前端 @ark-mate/desktop-contract 的 STREAM_ATTACH_DEADLINE_MS 同值）。
const defaultAttachDeadline = credential.StreamAttachDeadline

// Options WS 控制器装配选项（M1-1）。
type Options struct {
	// Credentials 凭据表。非空（Required）时，连接必须在 AttachDeadline 内发出首帧
	// `attach` 并携带具备 **stream 面**的凭据；否则服务端主动关闭（1008）。
	// 空表 = 不鉴权（历史默认行为）。
	Credentials *credential.Set
	// AttachDeadline 首帧 attach 截止时限；0 取 defaultAttachDeadline（仅测试会改小）。
	AttachDeadline time.Duration
	// OriginCheck 握手来源判定（M3-5）：由装配层注入的**同一个**策略
	// （middleware.OriginPolicy.CheckFunc），与 REST 中间件共用同一份 Host + Origin 判定。
	//
	// 在 websocket.Accept **之前**调用：返回非 nil error 即拒绝握手（不升级连接），
	// 按 status 回统一响应信封（错误已带业务码）。nil = 不判定（历史行为 / 策略未启用）。
	//
	// 为什么不挂到 AcceptOptions 上：本项目用的 coder/websocket v1.8.15 的 AcceptOptions
	// **没有 CheckOrigin 字段**（只有 InsecureSkipVerify 与 OriginPatterns），而库自带的
	// authenticateOrigin 把「Origin 的 host == 请求 Host」当同源放行——`Host: evil.com` +
	// `Origin: http://evil.com` 这种 DNS rebinding 形状会从它手里过。所以判定必须由本层在
	// 升级前完成；Accept 那边继续 InsecureSkipVerify（那是"库的判定不参与"，不是"不判定"）。
	OriginCheck func(r *http.Request) (status int, err error)
}

// wsCtr 是实时事件通道（WS）的协议薄适配层：
// 订阅 eventbus → 按 attach 的聚合（session id）推送 event/snapshot 帧；
// 客户端帧 action=attach/detach/ping；服务端帧 type=snapshot/event/detached/pong。
//
// 凭据模型（M1-1）：**握手不带凭据、连接后带内交付**——这是"令牌不进 URL"的代价与解法。
// 因此必须有首帧 attach 时限，否则未鉴权的连接会一直占着订阅位。
//
// 传输层使用 github.com/coder/websocket（context 化 API：Read/Write/Ping 带 ctx，
// 控制帧（ping/pong/close）由库自动应答，Ping 需与 Reader 并发执行，见其 conn.go 注释）。
// 契约见 docs/architecture.md §6.2。
type wsCtr struct {
	bus   *eventbus.Bus
	creds *credential.Set
	// attachDeadline 首帧 attach 截止（见 Options.AttachDeadline）。
	attachDeadline time.Duration
	// originCheck 握手来源判定（M3-5；见 Options.OriginCheck）。nil = 不判定。
	originCheck func(r *http.Request) (status int, err error)
}

// NewWSCtr 构造 WSCtr。
func NewWSCtr(bus *eventbus.Bus, opts ...Options) WSCtr {
	c := &wsCtr{bus: bus, attachDeadline: defaultAttachDeadline}
	if len(opts) > 0 {
		c.creds = opts[0].Credentials
		c.originCheck = opts[0].OriginCheck
		if opts[0].AttachDeadline > 0 {
			c.attachDeadline = opts[0].AttachDeadline
		}
	}
	return c
}

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second // 协议层 ping 的 pong 等待上限（探测死连接）
	wsPingPeriod     = 30 * time.Second // 协议层心跳间隔（防代理空闲断连）
	wsMaxMessageSize = 4096
)

// wsClientFrame 客户端 → 服务端帧。
type wsClientFrame struct {
	Action string `json:"action"` // attach | detach | ping
	AggID  string `json:"agg_id"` // attach/detach 目标聚合（session id）
	// Token 首帧 attach 携带的 stream 面凭据（M1-1；桌面态必需，独立态可选）。
	// 形状的唯一真源在前端 @ark-mate/desktop-contract 的 buildAttachFrame。
	Token string `json:"token"`
	// SinceSeq attach 续传游标（**会话内**位次）：省略或负数 = 从该会话第一条起全量回放
	// （-1 是哨兵）；`>= 0` = 从该条之后增量续传。0 是合法位次（`session.created`），
	// 不能当"没有游标"用。
	SinceSeq *int64 `json:"since_seq,omitempty"`
}

// wsServerFrame 服务端 → 客户端帧。
type wsServerFrame struct {
	Type     string          `json:"type"`                // snapshot | event | gap | detached | pong
	AggID    string          `json:"agg_id,omitempty"`    // 归属聚合
	EventSeq int64           `json:"event_seq,omitempty"` // 该会话水位（snapshot）/ event 的会话内位次 / gap 的首丢位次
	FromSeq  int64           `json:"from_seq,omitempty"`  // gap：重挂起点（= 首丢 seq）
	Event    *eventbus.Event `json:"event,omitempty"`     // event 帧载荷（id/type/seq/agg_id/version/payload；`id` 只对 durable 事件存在）
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

	// handshake 在首帧 attach 通过凭据校验后关闭（幂等）；awaitAttach 到点仍未关闭即关闭连接。
	handshake     chan struct{}
	handshakeOnce sync.Once

	closed    chan struct{}
	closeOnce sync.Once
}

// handshakeDone 首帧握手是否已完成。
func (st *connState) handshakeDone() bool {
	select {
	case <-st.handshake:
		return true
	default:
		return false
	}
}

// completeHandshake 标记首帧握手完成（重复调用安全）。
func (st *connState) completeHandshake() { st.handshakeOnce.Do(func() { close(st.handshake) }) }

// Handle 处理一次 WS 升级与生命周期。
func (h *wsCtr) Handle(c *gin.Context) {
	// 来源判定先于升级（M3-5）：错误的 Host 或来源在**握手**这一层就被拒，而不是先把连接
	// 建起来再关（后者已经把订阅位与连接资源给出去了）。判定与 REST 共用同一个策略对象。
	if h.originCheck != nil {
		if status, err := h.originCheck(c.Request); err != nil {
			c.Abort()
			gincontext.FailWithStatus(c, status, err)
			return
		}
	}
	// InsecureSkipVerify 的取舍见 Options.OriginCheck：库自带的 origin 校验比本层的矩阵**更松**
	// （同 Host 即放行），不能作为闸门；真正的闸门是上面那次 Decide。
	ws, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		glog.Debugw(c.Request.Context(), "ws upgrade failed", "err", err)
		return
	}
	defer ws.CloseNow()

	st := &connState{
		conn:      ws,
		write:     make(chan wsServerFrame, 256),
		attached:  make(map[string]bool),
		handshake: make(chan struct{}),
		closed:    make(chan struct{}),
	}
	st.ctx, st.cancel = context.WithCancel(context.Background())
	defer h.shutdown(st)

	ws.SetReadLimit(wsMaxMessageSize)

	// live 订阅只要增量（v21）：attach 之前的缺口由 EventsAfter(agg, since) 显式重放，
	// 两条路径各管一段、不重叠。此前这里是 `Subscribe(bus.LastSeq())`（"全局水位之后的"），
	// 而全局水位在 v21 之后不存在了——**每会话各自一个游标**这件事现在由两条路径的分工表达。
	live := h.bus.Subscribe()
	defer h.bus.Unsubscribe(live)

	go h.writeLoop(st)
	go h.pingLoop(st)
	go h.livePump(st, live)
	go h.awaitAttach(st)

	h.readLoop(st)
}

// awaitAttach 首帧 attach 截止（M1-1）：到点仍未完成握手即按 1008 关闭。
//
// 为什么必须有时限：令牌不进 URL 意味着"连接先建立、凭据随后到"，这段窗口里连接
// 已经占着 eventbus 的订阅位；没有时限就等于给了未鉴权连接一个常驻资源。
func (h *wsCtr) awaitAttach(st *connState) {
	t := time.NewTimer(h.attachDeadline)
	defer t.Stop()
	select {
	case <-st.closed:
	case <-st.handshake:
	case <-t.C:
		glog.Warnw(st.ctx, "ws 首帧 attach 超时，关闭连接", "deadline", h.attachDeadline)
		h.closeWithStatus(st, websocket.StatusPolicyViolation, "attach deadline exceeded")
	}
}

// authorizeStream 校验首帧 attach 携带的凭据是否具备 stream 面。
// 未配置任何凭据（独立态默认）→ 一律通过：那时这条通道本就是回环内开放的。
func (h *wsCtr) authorizeStream(token string) bool {
	if !h.creds.Required() {
		return true
	}
	return h.creds.Authorize(token, credential.ScopeStream)
}

// rejectHandshake 握手失败：记一行（不含令牌值）后以 1008 关闭。
func (h *wsCtr) rejectHandshake(st *connState, reason string) {
	glog.Warnw(st.ctx, "ws 首帧 attach 被拒，关闭连接", "reason", reason)
	h.closeWithStatus(st, websocket.StatusPolicyViolation, reason)
}

// closeWithStatus 主动关闭：先发带状态码的 close 帧（客户端可据此判定 1008），再收内务。
// coder/websocket 允许 Close 与 Read 并发（Close 会让 Read 返回错误），shutdown 再取消
// ctx 兜底解除其余阻塞。
func (h *wsCtr) closeWithStatus(st *connState, status websocket.StatusCode, reason string) {
	_ = st.conn.Close(status, reason)
	h.shutdown(st)
}

// readLoop 读取客户端帧并执行 attach/detach/ping；出错即结束整条连接。
// coder/websocket 会在 Read 内自动应答协议层 ping/pong/close 控制帧，
// 此处只处理应用层 JSON 帧（二进制帧忽略）。
//
// 首帧必须是 `attach`（握手）：它承载 stream 面凭据。通过之后本连接才进入订阅语义，
// 后续帧与 M0 一致（attach/detach/ping）。
func (h *wsCtr) readLoop(st *connState) {
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
		if !st.handshakeDone() {
			if f.Action != "attach" {
				h.rejectHandshake(st, "first frame must be attach")
				return
			}
			if !h.authorizeStream(f.Token) {
				h.rejectHandshake(st, "invalid stream token")
				return
			}
			st.completeHandshake()
		}
		switch f.Action {
		case "attach":
			// 通道级 attach（握手帧本身，无 agg_id）没有订阅动作；带 agg_id 的首帧
			// 视为"握手 + 订阅"一次完成（容错，见 buildAttachFrame 的调用方约定）。
			if f.AggID == "" {
				continue
			}
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
//   - `since_seq` 不传或 `< 0`（**-1 是"从最早一条起"的哨兵**）→ `EventsAfter(agg, -1)`
//     全量回放该聚合（UI 冷开启既有会话走这条：把会话视图从第一条事件重建起来）+ 后续 live；
//   - `since_seq >= 0` → 从该位置之后增量续传（断线重连传本地 lastSeq）。
//
// v21 之前 `since_seq` 省略是 snapshot（空 state + 水位）、传 0 表示"全量"，而 0 现在是一个
// **真实存在的位次**（该会话第一条事件 `session.created`）——把 0 当"从头"会让客户端每次冷挂载
// 都恰好丢掉会话的第一帧（会话标题/创建时间就在那一帧里）。
// 落在缓冲淘汰水位之前的请求仍回 snapshot（§5.2）。
//
// 简化说明：attach 的重放与 live 分属两条入队路径，极端并发下事件可能交错；
// M1 的 §4.5 冻结面（持锁原子决策 + 单写者重放）再收敛顺序。
func (h *wsCtr) attach(st *connState, f wsClientFrame) {
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

	since := int64(-1)
	if f.SinceSeq != nil {
		since = *f.SinceSeq
	}
	if since < 0 {
		since = -1 // 归一：任何负值都是"从该会话最早一条起"
	}
	// 淘汰即快照回退（§5.2）：请求起点早于该聚合的缓冲淘汰水位 → 该区间已不可增量续传，
	// 回整包 snapshot（M0/M1 snapshot 为空终态 + 当前水位，客户端据此重建游标并清空视图）。
	// 水位 -1 表示该聚合从未淘汰过；v21 之前这里是 `low > 0`，而 0 现在是个合法位次
	// （淘汰掉会话第 0 条同样意味着"不可续传"），用 0 当"没淘汰"会静默漏判。
	if low := h.bus.EvictedThrough(f.AggID); low >= 0 && since < low {
		h.send(st, wsServerFrame{Type: "snapshot", AggID: f.AggID, EventSeq: h.bus.LastSeq(f.AggID), State: json.RawMessage(`{}`)})
		return
	}
	for _, evt := range h.bus.EventsAfter(f.AggID, since) {
		h.send(st, wsServerFrame{Type: "event", AggID: f.AggID, EventSeq: evt.Seq, Event: &evt})
	}
}

func (h *wsCtr) detach(st *connState, aggID string) {
	st.mu.Lock()
	delete(st.attached, aggID)
	st.mu.Unlock()
	h.send(st, wsServerFrame{Type: "detached", AggID: aggID, Reason: "detached"})
}

// livePump 把 bus live 增量按已 attach 聚合过滤后投递到写队列；
// 慢订阅丢帧（bus gap 提示）→ 转 gap 帧，客户端据此立即重挂该聚合（按水位重放续传）。
func (h *wsCtr) livePump(st *connState, live *eventbus.Subscription) {
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
func (h *wsCtr) writeLoop(st *connState) {
	for {
		select {
		case <-st.closed:
			return
		case frame := <-st.write:
			b, err := json.Marshal(frame)
			if err != nil {
				glog.Debugw(st.ctx, "ws marshal failed", "type", frame.Type, "err", err)
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
func (h *wsCtr) pingLoop(st *connState) {
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
				glog.Debugw(st.ctx, "ws ping failed, closing", "err", err)
				h.shutdown(st)
				return
			}
		}
	}
}

// send 非阻塞入队（队列满视为消费者过慢，丢帧由 attach 续传/快照兜底）。
func (h *wsCtr) send(st *connState, f wsServerFrame) {
	select {
	case st.write <- f:
	default:
		glog.Debugw(st.ctx, "ws write queue full, dropping frame", "type", f.Type)
	}
}

// shutdown 幂等关闭连接内务：广播 closed（停循环）+ 取消连接 ctx（解除 Read/Write/Ping 阻塞）。
func (h *wsCtr) shutdown(st *connState) {
	st.closeOnce.Do(func() {
		close(st.closed)
		st.cancel()
	})
}
