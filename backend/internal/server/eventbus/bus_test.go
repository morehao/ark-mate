package eventbus

import (
	"testing"
)

// TestPublishSeqAndReplay 验证全局 seq 单调、Subscribe(after) 回放历史 + live 增量。
func TestPublishSeqAndReplay(t *testing.T) {
	b := New()
	e1 := b.Publish("session.message", "s1", 1, map[string]any{"text": "a"})
	e2 := b.Publish("session.thought", "s1", 1, map[string]any{"text": "b"})

	if e2.Seq != e1.Seq+1 {
		t.Fatalf("seq 不单调: e1=%d e2=%d", e1.Seq, e2.Seq)
	}

	sub := b.Subscribe(0) // after=0 → 回放全部历史
	defer b.Unsubscribe(sub)
	got := []Event{<-sub.C, <-sub.C}
	if got[0].Type != "session.message" || got[1].Type != "session.thought" {
		t.Fatalf("回放顺序错误: %v", got)
	}

	e3 := b.Publish("session.message", "s1", 2, map[string]any{"text": "live"})
	if gotLive := <-sub.C; gotLive.Seq != e3.Seq {
		t.Fatalf("live 增量缺失: got seq=%d want %d", gotLive.Seq, e3.Seq)
	}
}

// TestEventsAfter 验证快照式取历史（供 WS attach 重放）。
func TestEventsAfter(t *testing.T) {
	b := New()
	b.Publish("session.message", "s1", 1, nil) // seq 1
	b.Publish("session.thought", "s1", 1, nil) // seq 2
	b.Publish("session.message", "s2", 1, nil) // seq 3

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
	b := NewWithLimit(2)                       // 保留上限 2 条
	b.Publish("session.message", "s1", 1, nil) // seq 1
	b.Publish("session.message", "s1", 1, nil) // seq 2
	if b.EvictedThrough("s1") != 0 {
		t.Fatalf("前两条不应淘汰: %d", b.EvictedThrough("s1"))
	}
	b.Publish("session.message", "s1", 1, nil) // seq 3 → 淘汰 seq1
	if w := b.EvictedThrough("s1"); w != 1 {
		t.Fatalf("EvictedThrough(s1)=%d want 1", w)
	}
	b.Publish("session.message", "s2", 1, nil) // seq 4 → 淘汰 seq2（s1 水位 → 2）
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
