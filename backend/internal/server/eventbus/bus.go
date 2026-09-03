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

// Subscription 是一次事件流订阅：内含历史回放 + 实时增量通道。
type Subscription struct {
	C chan Event // 消费通道（复用 Bus 的历史 + 增量投递）
}

// Bus 是单一通道的事件总线。
type Bus struct {
	mu      sync.RWMutex
	nextSeq int64
	history []Event // 内存历史缓冲，用于 after 续传回放
	subs    map[*Subscription]struct{}
}

// New 创建事件总线。
func New() *Bus {
	return &Bus{nextSeq: 1, history: make([]Event, 0), subs: make(map[*Subscription]struct{})}
}

// Publish 向总线发布一个事件：分配 seq、写入历史缓冲并广播给全部订阅者。
// 订阅者消费不及时则丢弃该事件，由断线后以 after 续传兜底。
func (b *Bus) Publish(typ, aggID string, version int64, payload any) Event {
	b.mu.Lock()
	evt := Event{Type: typ, Seq: b.nextSeq, AggregID: aggID, Version: version, Payload: payload}
	b.nextSeq++
	b.history = append(b.history, evt)
	for sub := range b.subs {
		select {
		case sub.C <- evt:
		default:
		}
	}
	b.mu.Unlock()
	return evt
}

// Subscribe 订阅事件流。after 为起始游标：先同步回放 history 中 seq 大于 after 的历史，
// 再持续接收后续 live 增量。返回的订阅需在结束后调用 Unsubscribe 释放。
func (b *Bus) Subscribe(after int64) *Subscription {
	sub := &Subscription{C: make(chan Event, 256)}
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
