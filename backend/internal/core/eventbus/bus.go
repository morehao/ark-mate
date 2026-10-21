// Package eventbus 实现进程内会话事件总线（单一事件通道，与传输无关）。
// 设计对应架构文档 §2 实时会话事件流范式（载荷经 WS 通道推送，见设计文档 §4.5）：
//   - 单一通道 + type 判别 union：事件为带判别字段的 tagged union
//   - 可重放会话流：**每聚合**位次单调递增，订阅方带 since_seq 游标续传，支持断线重连
//   - 缓冲：事件先落内存历史缓冲，再分发给 live 订阅者，断连后可从 since 续拉
//
// **v21（2026-10-14）把位次从"全局"改成"每聚合"**（设计
// `.dsh/docs/specs/2026-10-14-data-model-v21-design.md` §1/D1）：`seq` 只在**同一个
// `AggregID`（= 会话）内部**可比，0 是合法位次（该聚合的第一条事件），"还没有事件"用 `-1` 表达。
//
// 为什么全局序必须走：它唯一的消费者是"让 `PublishAt` 不倒推"，而那本来就是**每聚合**的事
// （`EvictedThrough(aggID)` 从第一天起就按聚合判）。留着全局序要付的代价是"单个会话内的 seq
// 有洞"——用户看到的「第一条消息的 seq 为什么不是 1」就是它。
//
// 由此产生两条**类型拦不住**的约定，读这个文件前先记住：
//
//	① 任何 `seq` 入参/出参都必须**与一个 aggID 配对**（`LastSeq(aggID)`、`EventsAfter(aggID, after)`）。
//	   把两个聚合的 seq 拿来比较（`a.Seq < b.Seq`）在 v21 之后**没有意义**，而它不会编译报错。
//	② 哨兵只有 `-1`（= 早于该聚合的一切事件）。`0` 指向**真实存在的**第一条事件
//	   （`session.created`），把它当"没有游标"会让客户端每次冷挂载都丢掉会话的第一帧。
package eventbus

import "sync"

// Event 是总线上的单一事件载荷（type 为判别字段）。
type Event struct {
	// ID 行身份（`evt_<26 位 ULID>`；v17）。**只有 durable 事件有它**——落 events 表 / 进
	// transcript 的那些；automation 触发结果这类"只走实时通道"的事件（见 README 的
	// `automation.run`）刻意留空，于是 `omitempty` 让"帧上有 id ⟺ 它在 durable 日志里查得到"
	// 成为一条免费的不变式。
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`    // 事件类型（token/thought/tool_call/message/终态等）
	Seq      int64  `json:"seq"`     // **所属聚合内**的位次（0 起、稠密），用于断线续传
	AggregID string `json:"agg_id"`  // 聚合 ID（如 session id），对应 durable.aggregateID
	Version  int64  `json:"version"` // 聚合版本，对应 durable.version
	Payload  any    `json:"payload"` // 事件负载
}

// GapNotice 一条「慢订阅丢帧」提示：消费者落后导致事件被丢弃时发出，
// 携带首个被丢事件的 agg 与 seq（消费端据此重挂续传；修复丢帧无补偿）。
type GapNotice struct {
	Seq      int64  `json:"seq"`
	AggregID string `json:"agg_id"`
}

// Subscription 是一次事件流订阅：内含历史回放 + 实时增量通道；
// 慢订阅丢帧经 gaps 通道提示（容量 16，超限只保留最新缺口——丢帧重挂只需最新水位）。
type Subscription struct {
	C    chan Event     // 消费通道（复用 Bus 的历史 + 增量投递）
	gaps chan GapNotice // 丢帧提示（可选消费；未消费不影响事件流）
}

// Gap 返回该订阅的丢帧提示通道（无丢帧时零事件）。
func (s *Subscription) Gap() <-chan GapNotice { return s.gaps }

// Bus 是单一通道的事件总线。
type Bus struct {
	mu sync.RWMutex
	// nextSeq 每聚合的**下一个可用位次**（缺键 = 0，即该聚合还没发布过任何事件：
	// 第一条事件的位次是 0——`session.created` 就是这个 0）。
	//
	// 为什么是 map 而不是一个 int64：位次是聚合内的量（见包注释）。一个全局计数器既表达不了
	// "每个会话各自从 0 数起"，也正是「第一条消息的 seq 不是 1」那个症状的来源。
	nextSeq map[string]int64
	// published 已发布的事件**条数**（诊断与测试用）。
	//
	// 它是**计数，不是位次**：v21 起"最后一个 seq"在空总线与"刚发布了第一条"两种状态下都是 0，
	// 拿它判断"有没有发布过"会静默失效。要问"发布过没有"就问这里。
	published int64
	history   []Event          // 内存历史缓冲（按聚合淘汰，见 NewWithLimit），用于 since 续传回放
	limit     int              // history 保留上限（条数；淘汰即快照回退信号，见 §5.2/§5.7）
	evicted   map[string]int64 // 每聚合已淘汰的最大 seq（缺键 = 该聚合未淘汰）；low 水位供实时通道判定快照回退
	subs      map[*Subscription]struct{}
}

// defaultHistoryLimit 默认历史缓冲条数上限。超限淘汰最旧事件并抬高该聚合
// 的淘汰水位（evicted[agg]）：实时通道 attach 的 since 若低于水位 → 回 snapshot
// （§5.2「since_seq 落后于缓冲淘汰点 → 回 snapshot」；M1 内存版，M2 落库后由 events 表兜底）。
const defaultHistoryLimit = 65536

// New 创建事件总线（默认历史上限）。
func New() *Bus {
	return NewWithLimit(defaultHistoryLimit)
}

// NewWithLimit 创建事件总线并指定 history 保留上限（≤0 时按默认）。
func NewWithLimit(n int) *Bus {
	if n <= 0 {
		n = defaultHistoryLimit
	}
	return &Bus{
		nextSeq: make(map[string]int64),
		limit:   n,
		evicted: make(map[string]int64),
		subs:    make(map[*Subscription]struct{}),
	}
}

// Publish 向总线发布一个事件：分配**本聚合**的下一个位次、写入历史缓冲并广播给全部订阅者。
// 缓冲超限时淘汰最旧事件，并把其 seq 记为对应聚合的淘汰水位。
// 订阅者消费不及时则丢弃该事件，由断线后以 since 续传兜底。
//
// id 是事件的行身份（可空：只走实时通道的事件没有 durable 身份）——它不参与任何判据，
// 只跟着事件流到帧/历史/导出，让"某一帧"可被单独引用。
func (b *Bus) Publish(id, typ, aggID string, version int64, payload any) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishLocked(id, typ, aggID, version, payload, b.nextSeq[aggID])
}

// PublishAt 以显式 seq 发布事件（SQLite 模式下 DB 是**该会话**位次的权威：调用方已从事务内
// 取号，bus 仅镜像——重启恢复注入既有事件与 live 新事件共用同一条号序）。
//
// `aggID` 是**第一个参数**（不是最后一个）：位次离开聚合就没有意义（包注释 ①），
// 把它排在 seq 前面，就没有"传了 seq 却忘了说是谁的"这种调用形状可写。
//
// `seq` 小于该聚合当前水位时回落为聚合内取号（防取号异常导致重复/倒退）；**不小于时原样使用**
// （于是恢复注入 `seq = 0` 的第一条事件不会被改写成别的号）。
//
// id 同 Publish：`PublishAt` 的调用点都握着 durable 事件（DB 行 / transcript 行），
// 所以它必须把同一支 id 一起恢复——这是 v17「身份与位置一起随数据流动」的落点。
func (b *Bus) PublishAt(aggID string, seq int64, id, typ string, version int64, payload any) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq < b.nextSeq[aggID] {
		seq = b.nextSeq[aggID]
	}
	return b.publishLocked(id, typ, aggID, version, payload, seq)
}

// publishLocked 以给定 seq 完成「写入缓冲 → 淘汰 → 广播」并推进该聚合的 nextSeq（须持 b.mu）。
func (b *Bus) publishLocked(id, typ, aggID string, version int64, payload any, seq int64) Event {
	if seq < b.nextSeq[aggID] {
		seq = b.nextSeq[aggID]
	}
	evt := Event{ID: id, Type: typ, Seq: seq, AggregID: aggID, Version: version, Payload: payload}
	b.nextSeq[aggID] = seq + 1
	b.published++
	b.history = append(b.history, evt)
	for len(b.history) > b.limit {
		old := b.history[0]
		b.history = b.history[1:]
		if cur, ok := b.evicted[old.AggregID]; !ok || old.Seq > cur {
			b.evicted[old.AggregID] = old.Seq
		}
	}
	for sub := range b.subs {
		select {
		case sub.C <- evt:
		default:
			// 慢订阅：缓冲区满 → 丢帧并提示（gap 只保留最新缺口；丢帧由消费端重挂续传兜底）。
			n := GapNotice{Seq: evt.Seq, AggregID: evt.AggregID}
			select {
			case sub.gaps <- n:
			default:
				select {
				case <-sub.gaps: // 挤掉旧缺口
				default:
				}
				select {
				case sub.gaps <- n:
				default:
				}
			}
		}
	}
	return evt
}

// EvictedThrough 返回某聚合已被淘汰的最大 seq（**-1 = 该聚合尚无淘汰**）：
// 重放请求的 since 若低于该水位，说明请求区间起点已不在缓冲内 → 应回 snapshot。
//
// 哨兵是 `-1` 而不是 v21 之前的 `0`：`0` 现在是一个**真实存在**的位次（该聚合第一条事件），
// 淘汰掉它同样意味着"起点不可增量续传"，用 0 当"没淘汰"会让这种情形静默漏判。
func (b *Bus) EvictedThrough(aggID string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.evicted[aggID]
	if !ok {
		return -1
	}
	return v
}

// LastSeq 返回某聚合已发布的最后一个位次（**-1 = 该聚合还没有任何事件**）。
//
// 调用方拿它做两件事：① 快照帧携带"该会话当前水位"；② `PublishAt` 的对齐基准。
// 它**不是**跨会话可比的量（v21）。
func (b *Bus) LastSeq(aggID string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lastSeqLocked(aggID)
}

// lastSeqLocked 是 LastSeq 的持锁版本（publish 路径内部用，避免重入 RWMutex）。
func (b *Bus) lastSeqLocked(aggID string) int64 {
	if v, ok := b.nextSeq[aggID]; ok {
		return v - 1
	}
	return -1
}

// Published 返回已发布的事件**条数**（不是位次；诊断与测试用，见字段注释）。
func (b *Bus) Published() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.published
}

// Subscribe 订阅**live 增量**（不回放历史）。
//
// WS 通道用它：attach 之前的缺口由 `EventsAfter(agg, since)` 显式重放（两条路径各管一段，
// 不会互相重叠）。需要"回放 + live"的场景用 SubscribeAfter。
func (b *Bus) Subscribe() *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &Subscription{C: make(chan Event, 256), gaps: make(chan GapNotice, 16)}
	b.subs[sub] = struct{}{}
	return sub
}

// SubscribeAfter 订阅并**回放**历史：`after` 是"aggID → 该聚合已知水位"（缺键或 nil = -1
// = 从该聚合第一条起回放全部），随后继续接收 live 增量。
//
// 为什么入参是 map 而不是一个 int64：位次是聚合内的量（见包注释）。单个 int64 只能表达
// "全局水位"，而那个概念在 v21 之后不存在了——把 map 写进入参，是让"每个会话各自一个游标"
// 这件事在编译期就成立，而不是靠调用方记得。
func (b *Bus) SubscribeAfter(after map[string]int64) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	pass := func(evt Event) bool { return evt.Seq > floorOf(after, evt.AggregID) }
	// 缓冲按「回放条数 + 256 增量余量」分配：回放是**同步**投递（订阅者协程此时还没起来），
	// 若历史条数超过固定缓冲，投递会阻塞在持锁状态 → 死锁（历史>256 且游标很小时必现）。
	n := 0
	for _, evt := range b.history {
		if pass(evt) {
			n++
		}
	}
	sub := &Subscription{C: make(chan Event, n+256), gaps: make(chan GapNotice, 16)}
	for _, evt := range b.history {
		if pass(evt) {
			sub.C <- evt
		}
	}
	b.subs[sub] = struct{}{}
	return sub
}

// floorOf 返回某聚合的回放起点（缺键 → -1 = 从最早的开始）。
func floorOf(after map[string]int64, aggID string) int64 {
	if v, ok := after[aggID]; ok {
		return v
	}
	return -1
}

// Unsubscribe 从总线移除订阅，停止后续广播。
func (b *Bus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

// EventsAfter 返回**某聚合** seq > after 的历史事件快照（拷贝，供实时通道按聚合做重放）。
// `after = -1` = 从该聚合第一条起。
func (b *Bus) EventsAfter(aggID string, after int64) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, 0, len(b.history))
	for _, evt := range b.history {
		if evt.AggregID == aggID && evt.Seq > after {
			out = append(out, evt)
		}
	}
	return out
}

// EventsAll 返回全部历史事件快照（跨会话；`handoff`/导出/`mate demo` 这类"要看到所有会话"
// 的读面用）。**顺序是缓冲里的到达序**，不是任何全局位次序——v21 之后没有全局位次，
// 需要按会话分组有序的调用方自己按 (AggregID, Seq) 排。
func (b *Bus) EventsAll() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, len(b.history))
	copy(out, b.history)
	return out
}
