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
