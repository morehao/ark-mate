package svcstream

import "github.com/morehao/ark-mate/internal/core/eventbus"

// Stream 实时事件流的订阅端口（装配根持有 Bus，控制器只认这个接口）。
//
// 为什么存在：v3.1 §3.7 要求 `ctrws` **不直接订阅 bus**——订阅语义（历史回放 + `Gap()` 丢帧
// 提示 + 淘汰水位 + `LastSeq` 快照）是事件流契约的一部分，集中在用例层才能保证 REST/WS/CLI
// 三条出口看到的是同一份语义。控制器仍可使用 `eventbus.Event`/`*Subscription` 这些**值类型**
// （它们是线上形状），但拿不到 `*eventbus.Bus` 的发布面。
type Stream interface {
	Subscribe() *eventbus.Subscription
	Unsubscribe(sub *eventbus.Subscription)
	EvictedThrough(aggID string) int64
	LastSeq(aggID string) int64
	EventsAfter(aggID string, after int64) []eventbus.Event
}

type stream struct{ bus *eventbus.Bus }

// New 构造实时事件流端口。
func New(bus *eventbus.Bus) Stream { return &stream{bus: bus} }

func (s *stream) Subscribe() *eventbus.Subscription { return s.bus.Subscribe() }

func (s *stream) Unsubscribe(sub *eventbus.Subscription) { s.bus.Unsubscribe(sub) }

func (s *stream) EvictedThrough(aggID string) int64 { return s.bus.EvictedThrough(aggID) }

func (s *stream) LastSeq(aggID string) int64 { return s.bus.LastSeq(aggID) }

func (s *stream) EventsAfter(aggID string, after int64) []eventbus.Event {
	return s.bus.EventsAfter(aggID, after)
}
