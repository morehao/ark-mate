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
	if got := b.PublishAt("s1", 7, testEventID, "session.message", 1, nil); got.ID != testEventID {
		t.Fatalf("PublishAt 丢了事件 id：got %q want %q", got.ID, testEventID)
	}
	// 只走实时通道的事件（automation.run 那类）刻意没有身份：空串**不算**丢。
	if got := b.Publish("", "automation.run", "a1", 1, nil); got.ID != "" {
		t.Fatalf("无身份事件不该凭空长出一个 id：got %q", got.ID)
	}
}

// TestPublishSeqIsPerAggregate 位次是**每聚合**的量（v21 的核心判据）：
// 两个会话交错发布，各自 seq 从 0 起、连续、互不影响；未发布过的聚合水位是 -1。
//
// 反证：把 `nextSeq` 改回单个 int64（v21 之前的形状）→ 第二个聚合的 seq 从 2 起，本用例红。
func TestPublishSeqIsPerAggregate(t *testing.T) {
	b := New()
	if got := b.LastSeq("s1"); got != -1 {
		t.Fatalf("没有事件的聚合水位应为 -1（不是 0——0 是真实位次）: %d", got)
	}
	type step struct {
		agg  string
		want int64
	}
	want := []step{
		{"s1", 0}, {"s2", 0}, {"s1", 1}, {"s2", 1}, {"s1", 2},
	}
	for i, w := range want {
		evt := b.Publish(testEventID, "session.message", w.agg, int64(i), nil)
		if evt.Seq != w.want {
			t.Fatalf("第 %d 次发布 agg=%s seq=%d want %d", i, w.agg, evt.Seq, w.want)
		}
	}
	if got := b.LastSeq("s1"); got != 2 {
		t.Fatalf("LastSeq(s1)=%d want 2", got)
	}
	if got := b.LastSeq("s2"); got != 1 {
		t.Fatalf("LastSeq(s2)=%d want 1", got)
	}
	// 每个聚合的历史各自稠密，且只含自己的事件。
	if got := b.EventsAfter("s1", -1); len(got) != 3 {
		t.Fatalf("s1 历史条数=%d want 3", len(got))
	}
	for _, evt := range b.EventsAfter("s2", -1) {
		if evt.AggregID != "s2" {
			t.Fatalf("EventsAfter(s2) 混进了别的聚合: %+v", evt)
		}
	}
}

// TestZeroIsAValidSeq 位次 0 是**真实存在的第一条事件**，不是"没有游标"的哨兵。
//
// 这是 2026-10-14 用户报的那个症状（「第一条消息的 seq 为什么不是 1」）在 bus 层的判据：
// v21 之前 seq 从 1 起、`0` 被借用成哨兵，于是"第 0 条"这个概念根本不存在；现在它存在，
// 而且必须能被显式恢复（`PublishAt(agg, 0, …)` 不得被改写）、被区间排除（`after=0`）。
func TestZeroIsAValidSeq(t *testing.T) {
	b := New()
	first := b.Publish(testEventID, "session.created", "s1", 1, nil)
	if first.Seq != 0 {
		t.Fatalf("会话第一条事件的 seq 应为 0（opencode/DSH 同形）: %d", first.Seq)
	}
	// 恢复注入：显式位次 0 必须原样镜像（此前 `PublishAt` 会把它当成"未取到号"往上顶）。
	mirrored := b.PublishAt("s2", 0, testEventID, "session.created", 1, nil)
	if mirrored.Seq != 0 {
		t.Fatalf("PublishAt 改写了显式位次 0: %d", mirrored.Seq)
	}
	// 开区间语义：-1 = 从最早一条起（含 0）；0 = 第 0 条之后（不含 0）。
	if got := b.EventsAfter("s1", -1); len(got) != 1 || got[0].Seq != 0 {
		t.Fatalf("EventsAfter(s1,-1) 应含第 0 条: %+v", got)
	}
	b.Publish(testEventID, "session.user_message", "s1", 1, nil) // seq 1
	if got := b.EventsAfter("s1", 0); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("EventsAfter(s1,0) 应跳过第 0 条: %+v", got)
	}
}

// TestSubscribeAfterReplayIsPerAggregate 回放游标是**每聚合**的：
// `SubscribeAfter(map{"s1":0})` 只回放 s1 中 seq > 0 的历史，随后 live 增量两个聚合都收。
func TestSubscribeAfterReplayIsPerAggregate(t *testing.T) {
	b := New()
	b.Publish(testEventID, "session.created", "s1", 1, map[string]any{"text": "s1-0"}) // s1 seq 0
	b.Publish(testEventID, "session.created", "s2", 1, map[string]any{"text": "s2-0"}) // s2 seq 0
	b.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "s1-1"}) // s1 seq 1
	b.Publish(testEventID, "session.message", "s2", 1, map[string]any{"text": "s2-1"}) // s2 seq 1
	sub := b.SubscribeAfter(map[string]int64{"s1": 0})
	defer b.Unsubscribe(sub)
	// 回放顺序 = 缓冲到达序（v21 之后没有全局位次可排，跨聚合的相对顺序本就不承载语义）。
	// 被游标挡掉的只有 s1 的 seq 0；s2 未登记在游标表里 → 从 -1 起回放全部（seq0、seq1）。
	got := []Event{<-sub.C, <-sub.C, <-sub.C}
	if got[0].AggregID != "s2" || got[0].Seq != 0 ||
		got[1].AggregID != "s1" || got[1].Seq != 1 ||
		got[2].AggregID != "s2" || got[2].Seq != 1 {
		t.Fatalf("回放集不符: %+v", got)
	}
	// live 增量继续到达（不因回放而停止）。
	live := b.Publish(testEventID, "session.message", "s2", 2, nil) // s2 seq 2
	if e := <-sub.C; e.Seq != live.Seq || e.AggregID != "s2" {
		t.Fatalf("live 增量缺失: %+v", e)
	}
}

// TestSubscribeLiveOnly 验证 `Subscribe()` 不回放历史（WS 的 live 通道靠它，
// attach 之前的缺口由 `EventsAfter(agg, since)` 显式重放）。
func TestSubscribeLiveOnly(t *testing.T) {
	b := New()
	b.Publish(testEventID, "session.created", "s1", 1, nil) // 历史
	sub := b.Subscribe()
	defer b.Unsubscribe(sub)
	select {
	case evt := <-sub.C:
		t.Fatalf("live 订阅不该回放历史: %+v", evt)
	default:
	}
	live := b.Publish(testEventID, "session.message", "s1", 1, nil)
	if e := <-sub.C; e.Seq != live.Seq {
		t.Fatalf("live 增量缺失: got seq=%d want %d", e.Seq, live.Seq)
	}
}

// TestEventsAfterAggregateScope 验证按聚合取快照（供 WS attach 重放）。
func TestEventsAfterAggregateScope(t *testing.T) {
	b := New()
	b.Publish(testEventID, "session.message", "s1", 1, nil) // s1 seq 0
	b.Publish(testEventID, "session.thought", "s1", 1, nil) // s1 seq 1
	b.Publish(testEventID, "session.message", "s2", 1, nil) // s2 seq 0

	if got := b.EventsAfter("s1", -1); len(got) != 2 || got[0].Seq != 0 || got[1].Seq != 1 {
		t.Fatalf("EventsAfter(s1,-1) = %+v", got)
	}
	if got := b.EventsAfter("s1", 0); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("EventsAfter(s1,0) = %+v", got)
	}
	if got := b.EventsAfter("s2", -1); len(got) != 1 || got[0].Seq != 0 {
		t.Fatalf("EventsAfter(s2,-1) = %+v", got)
	}
	if got := b.EventsAfter("nope", -1); len(got) != 0 {
		t.Fatalf("未知聚合应无历史: %+v", got)
	}
	if b.LastSeq("s1") != 1 {
		t.Fatalf("LastSeq(s1)=%d want 1", b.LastSeq("s1"))
	}
}

// TestEvictionWatermark 验证 per-agg 淘汰与低水位（EvictedThrough）：
// 缓冲超限淘汰最旧事件并抬高对应聚合水位；重放只能覆盖水位之后。
//
// 同时钉住哨兵：**没淘汰是 -1，不是 0**——`0` 是合法位次，淘汰掉会话第 0 条事件同样意味着
// "起点不可增量续传"，用 0 当"没淘汰"会让这一情形静默漏判（WS 就不会回 snapshot 了）。
func TestEvictionWatermark(t *testing.T) {
	b := NewWithLimit(2)                                    // 保留上限 2 条
	b.Publish(testEventID, "session.message", "s1", 1, nil) // s1 seq 0
	b.Publish(testEventID, "session.message", "s1", 1, nil) // s1 seq 1
	if got := b.EvictedThrough("s1"); got != -1 {
		t.Fatalf("前两条不应淘汰（应为 -1）: %d", got)
	}
	b.Publish(testEventID, "session.message", "s1", 1, nil) // s1 seq 2 → 淘汰 s1 seq0
	if got := b.EvictedThrough("s1"); got != 0 {
		t.Fatalf("EvictedThrough(s1)=%d want 0（淘汰的正是第 0 条）", got)
	}
	b.Publish(testEventID, "session.message", "s2", 1, nil) // s2 seq 0 → 淘汰 s1 seq1
	if got := b.EvictedThrough("s1"); got != 1 {
		t.Fatalf("EvictedThrough(s1)=%d want 1", got)
	}
	// 重放 s1 after=-1：只剩 s1 seq2
	if got := b.EventsAfter("s1", -1); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("淘汰后 EventsAfter(s1,-1) = %+v", got)
	}
	if got := b.EvictedThrough("s2"); got != -1 {
		t.Fatalf("s2 未淘汰（应为 -1）: %d", got)
	}
}

// TestPublishAt 验证显式 seq 发布（DB 为**会话内**位次权威的镜像路径）：
// 注入既有事件后进程内继续发布不重叠、游标连续；不同聚合各算各的。
func TestPublishAt(t *testing.T) {
	b := New()
	// 模拟重启恢复注入 DB 既有事件（s1 的 seq 0..2，来自旧进程持久化）。
	one := b.PublishAt("s1", 0, testEventID, "session.created", 1, map[string]any{"text": "old0"})
	two := b.PublishAt("s1", 1, testEventID, "session.thought", 1, map[string]any{"text": "old1"})
	three := b.PublishAt("s1", 2, testEventID, "run.ended", 2, map[string]any{})
	if one.Seq != 0 || two.Seq != 1 || three.Seq != 2 {
		t.Fatalf("PublishAt 未按显式 seq: %d %d %d", one.Seq, two.Seq, three.Seq)
	}
	if b.LastSeq("s1") != 2 {
		t.Fatalf("LastSeq(s1)=%d want 2", b.LastSeq("s1"))
	}
	// 后续进程内发布接续（live 新事件与恢复注入共用同一条号序）。
	four := b.PublishAt("s1", 3, testEventID, "session.user_message", 3, map[string]any{"text": "again"})
	if four.Seq != 3 {
		t.Fatalf("接续 seq 错误: %d", four.Seq)
	}
	if five := b.Publish(testEventID, "session.message", "s1", 3, nil); five.Seq != 4 {
		t.Fatalf("Publish 应与 PublishAt 共享号序: %d", five.Seq)
	}
	// 乱序/重复取号防倒退：显式 seq 小于该聚合水位时回落水位。
	six := b.PublishAt("s1", 1, testEventID, "session.message", 3, nil)
	if six.Seq != 5 {
		t.Fatalf("重复取号应回落聚合水位: %d", six.Seq)
	}
	// 另一个聚合不受影响：它从 0 起。
	if other := b.PublishAt("s2", 0, testEventID, "session.created", 1, nil); other.Seq != 0 {
		t.Fatalf("另一个聚合应从 0 起: %d", other.Seq)
	}
	// 回放连续性：s1 after=-1 得到注入 + live 全部 6 条。
	if got := b.EventsAfter("s1", -1); len(got) != 6 {
		t.Fatalf("s1 回放条数=%d want 6", len(got))
	}
}

// TestSlowSubscriberGapNotice 验证慢订阅丢帧补偿：
// 订阅不消费时缓冲区满后的 live 事件被丢，每次丢帧以 GapNotice 提示（带 agg 与首丢 seq）；
// 消费恢复后事件流继续（不丢已缓冲事件）、无丢帧时零提示。
func TestSlowSubscriberGapNotice(t *testing.T) {
	b := New()
	sub := b.Subscribe() // 发布前订阅 → 无历史回放
	defer b.Unsubscribe(sub)

	// 不消费 live：缓冲 256 满后开始丢帧并出 gap。位次从 0 起，所以第 257 条（seq 256）是首个被丢的。
	const total = 300
	for i := 0; i < total; i++ {
		b.Publish(testEventID, "session.message", "s1", int64(i), map[string]any{"i": i})
	}
	got, ok := <-sub.Gap()
	if !ok {
		t.Fatal("应收到 gap 提示")
	}
	// gap 通道只保留最新 16 个缺口，所以读到的不是"首个被丢的"（seq 256）而是缺口窗口的头部；
	// 判据只需保证它**确实越过了缓冲边界**（>= 256 = 至少丢了 seq256 那条）。
	if got.AggregID != "s1" || got.Seq < 256 {
		t.Fatalf("gap 内容不符（应 >= 256）: %+v", got)
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
	if e := b.Publish(testEventID, "session.message", "s1", 301, nil); e.Seq != total {
		t.Fatalf("seq 推进异常: %d want %d", e.Seq, total)
	}
	select {
	case ev := <-sub.C:
		if ev.Seq != total {
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
	sub := b.Subscribe()
	defer b.Unsubscribe(sub)
	for i := 0; i < 20; i++ {
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

// TestSubscribeReplayBeyondBuffer 回归：回放条数超过默认缓冲（256）时 SubscribeAfter 不得阻塞/死锁。
// 历史上 Subscribe 用固定 256 缓冲同步投递且持锁，历史 >256 且游标很小时直接死锁
// （`mate demo` 的 bus.Subscribe(0) 即触发点；v21 后对应 `SubscribeAfter(nil)`）。
func TestSubscribeReplayBeyondBuffer(t *testing.T) {
	b := New()
	const n = 1000
	for i := 0; i < n; i++ {
		b.Publish(testEventID, "token", "agg", int64(i+1), map[string]any{"i": i})
	}
	done := make(chan *Subscription, 1)
	go func() { done <- b.SubscribeAfter(nil) }()
	var sub *Subscription
	select {
	case sub = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SubscribeAfter 阻塞（回放超过缓冲导致死锁）")
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
