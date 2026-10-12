package eventbus

import (
	"testing"
	"time"
)

// testEventID 测试用的事件行身份。
//
// bus **不校验也不解释** id（它只是跟着事件走的一支值），所以这里用一个固定字面量就够；
// 真实形状由 objid 铸（`evt_<26 位 ULID>`），那部分的判据在 `pkg/object/objid` 与 archtest。
const testEventID = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1PD"

// TestPublishCarriesEventID 身份必须**原样**跟着事件走（Publish 与 PublishAt 两条路）。
//
// 这条是 v17「身份随数据流动」在 bus 这一层的最小判据：只要有一处把 id 忘了传（或落成空串），
// 帧上就没有 id，客户端就无法把"某一帧"引用出来，而这件事在其他任何测试里都不会红。
func TestPublishCarriesEventID(t *testing.T) {
	b := New()
	if got := b.Publish(testEventID, "session.message", "s1", 1, nil); got.ID != testEventID {
		t.Fatalf("Publish 丢了事件 id：got %q want %q", got.ID, testEventID)
	}
	if got := b.PublishAt(7, testEventID, "session.message", "s1", 1, nil); got.ID != testEventID {
		t.Fatalf("PublishAt 丢了事件 id：got %q want %q", got.ID, testEventID)
	}
	// 只走实时通道的事件（automation.run 那类）刻意没有身份：空串**不算**丢。
	if got := b.Publish("", "automation.run", "a1", 1, nil); got.ID != "" {
		t.Fatalf("无身份事件不该凭空长出一个 id：got %q", got.ID)
	}
}

// TestPublishSeqAndReplay 验证全局 seq 单调、Subscribe(after) 回放历史 + live 增量。
func TestPublishSeqAndReplay(t *testing.T) {
	b := New()
	e1 := b.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "a"})
	e2 := b.Publish(testEventID, "session.thought", "s1", 1, map[string]any{"text": "b"})

	if e2.Seq != e1.Seq+1 {
		t.Fatalf("seq 不单调: e1=%d e2=%d", e1.Seq, e2.Seq)
	}

	sub := b.Subscribe(0) // after=0 → 回放全部历史
	defer b.Unsubscribe(sub)
	got := []Event{<-sub.C, <-sub.C}
	if got[0].Type != "session.message" || got[1].Type != "session.thought" {
		t.Fatalf("回放顺序错误: %v", got)
	}

	e3 := b.Publish(testEventID, "session.message", "s1", 2, map[string]any{"text": "live"})
	if gotLive := <-sub.C; gotLive.Seq != e3.Seq {
		t.Fatalf("live 增量缺失: got seq=%d want %d", gotLive.Seq, e3.Seq)
	}
}

// TestEventsAfter 验证快照式取历史（供 WS attach 重放）。
func TestEventsAfter(t *testing.T) {
	b := New()
	b.Publish(testEventID, "session.message", "s1", 1, nil) // seq 1
	b.Publish(testEventID, "session.thought", "s1", 1, nil) // seq 2
	b.Publish(testEventID, "session.message", "s2", 1, nil) // seq 3

	after := b.EventsAfter(1) // >1：seq 2、3
	if len(after) != 2 || after[0].Seq != 2 || after[1].Seq != 3 {
		t.Fatalf("EventsAfter(1) = %+v", after)
	}
	if b.LastSeq() != 3 {
		t.Fatalf("LastSeq=%d want 3", b.LastSeq())
	}
}

// TestEvictionWatermark 验证 per-agg 淘汰与低水位（EvictedThrough）：
// 缓冲超限淘汰最旧事件并抬高对应聚合水位；重放只能覆盖水位之后。
func TestEvictionWatermark(t *testing.T) {
	b := NewWithLimit(2)                                    // 保留上限 2 条
	b.Publish(testEventID, "session.message", "s1", 1, nil) // seq 1
	b.Publish(testEventID, "session.message", "s1", 1, nil) // seq 2
	if b.EvictedThrough("s1") != 0 {
		t.Fatalf("前两条不应淘汰: %d", b.EvictedThrough("s1"))
	}
	b.Publish(testEventID, "session.message", "s1", 1, nil) // seq 3 → 淘汰 seq1
	if w := b.EvictedThrough("s1"); w != 1 {
		t.Fatalf("EvictedThrough(s1)=%d want 1", w)
	}
	b.Publish(testEventID, "session.message", "s2", 1, nil) // seq 4 → 淘汰 seq2（s1 水位 → 2）
	if w := b.EvictedThrough("s1"); w != 2 {
		t.Fatalf("EvictedThrough(s1)=%d want 2", w)
	}
	// 重放 after=1：仅剩 s1 seq3、s2 seq4
	after := b.EventsAfter(1)
	if len(after) != 2 || after[0].Seq != 3 || after[1].Seq != 4 {
		t.Fatalf("淘汰后 EventsAfter(1) = %+v", after)
	}
	// s2 未淘汰（seq4 仍在缓冲）
	if b.EvictedThrough("s2") != 0 {
		t.Fatalf("s2 不应淘汰: %d", b.EvictedThrough("s2"))
	}
}

// TestPublishAt 验证显式 seq 发布（DB 为 seq 权威的镜像路径）：
// 注入既有事件后进程内继续发布不重叠、游标连续。
func TestPublishAt(t *testing.T) {
	b := New()
	// 模拟重启恢复注入 DB 既有事件（seq 1..3，来自旧进程持久化）。
	one := b.PublishAt(1, testEventID, "session.message", "s1", 1, map[string]any{"text": "old1"})
	two := b.PublishAt(2, testEventID, "session.thought", "s1", 1, map[string]any{"text": "old2"})
	three := b.PublishAt(3, testEventID, "run.ended", "s1", 2, map[string]any{})
	if one.Seq != 1 || two.Seq != 2 || three.Seq != 3 {
		t.Fatalf("PublishAt 未按显式 seq: %d %d %d", one.Seq, two.Seq, three.Seq)
	}
	if b.LastSeq() != 3 {
		t.Fatalf("LastSeq=%d want 3", b.LastSeq())
	}
	// 后续进程内发布（DB 取号一致时也用 PublishAt；此处验证 nextSeq 接续）。
	four := b.PublishAt(4, testEventID, "session.user_message", "s1", 3, map[string]any{"text": "again"})
	if four.Seq != 4 {
		t.Fatalf("接续 seq 错误: %d", four.Seq)
	}
	if five := b.Publish(testEventID, "session.message", "s1", 3, nil); five.Seq != 5 {
		t.Fatalf("Publish 应与 PublishAt 共享号序: %d", five.Seq)
	}
	// 乱序/重复取号防倒退：显式 seq 小于 nextSeq 时回落 nextSeq。
	six := b.PublishAt(1, testEventID, "session.message", "s2", 1, nil)
	if six.Seq != 6 {
		t.Fatalf("重复取号应回落 nextSeq: %d", six.Seq)
	}
	// 回放连续性：after=0 得到注入 + live 全部 6 条。
	if got := b.EventsAfter(0); len(got) != 6 {
		t.Fatalf("回放条数=%d want 6", len(got))
	}
}

// TestSlowSubscriberGapNotice 验证慢订阅丢帧补偿：
// 订阅不消费时缓冲区满后的 live 事件被丢，每次丢帧以 GapNotice 提示（带 agg 与首丢 seq）；
// 消费恢复后事件流继续（不丢已缓冲事件）、无丢帧时零提示。
func TestSlowSubscriberGapNotice(t *testing.T) {
	b := New()
	sub := b.Subscribe(0) // 发布前订阅 → 无历史回放
	defer b.Unsubscribe(sub)

	// 不消费 live：缓冲 256 满后开始丢帧并出 gap。
	const total = 300
	for i := 1; i <= total; i++ {
		b.Publish(testEventID, "session.message", "s1", int64(i), map[string]any{"i": i})
	}
	got, ok := <-sub.Gap()
	if !ok {
		t.Fatal("应收到 gap 提示")
	}
	if got.AggregID != "s1" || got.Seq <= 256 {
		t.Fatalf("gap 内容不符: %+v", got)
	}
	// 恢复消费：先排空已缓冲事件，随后 live 增量照常到达。
	for {
		select {
		case <-sub.C:
			continue
		default:
		}
		break
	}
	if e := b.Publish(testEventID, "session.message", "s1", 301, nil); e.Seq != 301 {
		t.Fatalf("seq 推进异常: %d", e.Seq)
	}
	select {
	case ev := <-sub.C:
		if ev.Seq != 301 {
			t.Fatalf("恢复后应收到最新事件: %d", ev.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("恢复消费后应能收到 live 事件")
	}
	// 消费跟得上 → 不再产生 gap（先排空已缓冲的旧提示，再断言无新增）。
	for {
		select {
		case <-sub.Gap():
			continue
		default:
		}
		break
	}
	select {
	case g := <-sub.Gap():
		t.Fatalf("不应再有 gap: %+v", g)
	default:
	}
}

// TestNoGapOnPromptConsumer 消费跟得上的订阅全程零 gap 提示。
func TestNoGapOnPromptConsumer(t *testing.T) {
	b := New()
	sub := b.Subscribe(0)
	defer b.Unsubscribe(sub)
	for i := 1; i <= 20; i++ {
		b.Publish(testEventID, "session.message", "s1", 1, nil)
		select {
		case ev := <-sub.C:
			if ev.Seq != int64(i) {
				t.Fatalf("事件序错乱: got %d want %d", ev.Seq, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 个事件未到", i)
		}
	}
	select {
	case g := <-sub.Gap():
		t.Fatalf("不应有 gap: %+v", g)
	default:
	}
}

// TestSubscribeReplayBeyondBuffer 回归：回放条数超过默认缓冲（256）时 Subscribe 不得阻塞/死锁。
// 历史上 Subscribe 用固定 256 缓冲同步投递且持锁，历史 >256 且 after 小时直接死锁
// （`mate demo` 的 bus.Subscribe(0) 即触发点）。
func TestSubscribeReplayBeyondBuffer(t *testing.T) {
	b := New()
	const n = 1000
	for i := 0; i < n; i++ {
		b.Publish(testEventID, "token", "agg", int64(i+1), map[string]any{"i": i})
	}
	done := make(chan *Subscription, 1)
	go func() { done <- b.Subscribe(0) }()
	var sub *Subscription
	select {
	case sub = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe 阻塞（回放超过缓冲导致死锁）")
	}
	defer b.Unsubscribe(sub)
	got := 0
	for got < n {
		select {
		case <-sub.C:
			got++
		case <-time.After(5 * time.Second):
			t.Fatalf("回放只收到 %d/%d 条", got, n)
		}
	}
}
