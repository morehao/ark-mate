// Package eventbus 实现进程内会话事件总线（单一事件通道，与传输无关）。
// 设计对应架构文档 §2 实时会话事件流范式（载荷经 WS 通道推送，见设计文档 §4.5）：
//   - 单一通道 + type 判别 union：事件为带判别字段的 tagged union
//   - 可重放会话流：全局单调递增 seq，订阅方带 after/since_seq 游标续传，支持断线重连
//   - 缓冲：事件先落内存历史缓冲，再分发给 live 订阅者，断连后可从 after 续拉
package eventbus

import "sync"

// Event 是总线上的单一事件载荷（type 为判别字段）。
type Event struct {
	Type     string `json:"type"`    // 事件类型（token/thought/tool_call/message/终态等）
	Seq      int64  `json:"seq"`     // 全局单调递增序号，用于断线续传
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
	mu      sync.RWMutex
	nextSeq int64
	history []Event          // 内存历史缓冲（per-agg 淘汰，见 NewWithLimit），用于 after 续传回放
	limit   int              // history 保留上限（条数；淘汰即快照回退信号，见 §5.2/§5.7）
	evicted map[string]int64 // 每聚合已淘汰的最大 seq（0 = 该聚合未淘汰）；low 水位供实时通道判定快照回退
	subs    map[*Subscription]struct{}
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
		nextSeq: 1,
		limit:   n,
		evicted: make(map[string]int64),
		subs:    make(map[*Subscription]struct{}),
	}
}

// Publish 向总线发布一个事件：分配 seq、写入历史缓冲并广播给全部订阅者。
// 缓冲超限时淘汰最旧事件，并把其 seq 记为对应聚合的淘汰水位。
// 订阅者消费不及时则丢弃该事件，由断线后以 after 续传兜底。
func (b *Bus) Publish(typ, aggID string, version int64, payload any) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishLocked(typ, aggID, version, payload, b.nextSeq)
}

// PublishAt 以显式 seq 发布事件（P1 SQLite 模式下 DB 为全局 seq 权威：
// 调用方已从事务内取号，bus 仅镜像——重启恢复注入既有事件与 live 新事件共用同一条号序）。
// seq ≤0 或小于当前 nextSeq 时回落为进程内取号（防取号异常导致重复/倒退）。
func (b *Bus) PublishAt(seq int64, typ, aggID string, version int64, payload any) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq < b.nextSeq {
		seq = b.nextSeq
	}
	return b.publishLocked(typ, aggID, version, payload, seq)
}

// publishLocked 以给定 seq 完成「写入缓冲 → 淘汰 → 广播」并推进 nextSeq（须持 b.mu）。
func (b *Bus) publishLocked(typ, aggID string, version int64, payload any, seq int64) Event {
	if seq < b.nextSeq {
		seq = b.nextSeq
	}
	evt := Event{Type: typ, Seq: seq, AggregID: aggID, Version: version, Payload: payload}
	b.nextSeq = seq + 1
	b.history = append(b.history, evt)
	for len(b.history) > b.limit {
		old := b.history[0]
		b.history = b.history[1:]
		if old.Seq > b.evicted[old.AggregID] {
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

// EvictedThrough 返回某聚合已被淘汰的最大 seq（0 = 尚无淘汰）：
// 重放请求的 since 若低于该水位，说明请求区间起点已不在缓冲内 → 应回 snapshot。
func (b *Bus) EvictedThrough(aggID string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.evicted[aggID]
}

// Subscribe 订阅事件流。after 为起始游标：先同步回放 history 中 seq 大于 after 的历史，
// 再持续接收后续 live 增量。返回的订阅需在结束后调用 Unsubscribe 释放。
func (b *Bus) Subscribe(after int64) *Subscription {
	sub := &Subscription{C: make(chan Event, 256), gaps: make(chan GapNotice, 16)}
	b.mu.Lock()
	for _, evt := range b.history {
		if evt.Seq > after {
			sub.C <- evt
		}
	}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()
	return sub
}

// Unsubscribe 从总线移除订阅，停止后续广播。
func (b *Bus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

// LastSeq 返回已发布的最后一个序号（供订阅方对齐游标/判断快照回退用）。
func (b *Bus) LastSeq() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nextSeq - 1
}

// EventsAfter 返回 seq > after 的历史事件快照（拷贝，供实时通道按聚合做重放）。
func (b *Bus) EventsAfter(after int64) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, 0, len(b.history))
	for _, evt := range b.history {
		if evt.Seq > after {
			out = append(out, evt)
		}
	}
	return out
}
