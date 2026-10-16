package session

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSink 收集合并后的发布（保序），用于直接驱动 Coalescer。
type fakeSink struct {
	mu   sync.Mutex
	typ  []string
	conv []string
	pay  []map[string]any
}

func (f *fakeSink) fn(typ, sessionID string, payload any) {
	m, _ := payload.(map[string]any)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typ = append(f.typ, typ)
	f.conv = append(f.conv, sessionID)
	f.pay = append(f.pay, m)
}

func (f *fakeSink) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.pay))
	for _, p := range f.pay {
		s, _ := p["text"].(string)
		out = append(out, s)
	}
	return out
}

func (f *fakeSink) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pay)
}

// TestChunkCoalescerMergesSameShape 同形（除 text 外字段一致）的连续增量合并成一条：
// 事件类型/会话不变，payload 形状不变（msg_id 等原样），text 为拼接结果；入参不被就地改写。
func TestChunkCoalescerMergesSameShape(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()

	base := map[string]any{"session_id": "s1", "runtime": "rtx", "msg_id": "m1", "text": "a"}
	c.Add(BusMessage, "s1", base)
	c.Add(BusMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "msg_id": "m1", "text": "b"})
	c.Add(BusMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "msg_id": "m1", "text": "c"})
	if got := sink.n(); got != 0 {
		t.Fatalf("未达阈值不应发布: %d", got)
	}
	c.FlushAll()
	if got := sink.texts(); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("应合并成一条 abc: %v", got)
	}
	if sink.typ[0] != BusMessage || sink.conv[0] != "s1" {
		t.Fatalf("类型/会话不符: %s %s", sink.typ[0], sink.conv[0])
	}
	if sink.pay[0]["msg_id"] != "m1" || sink.pay[0]["runtime"] != "rtx" {
		t.Fatalf("形状字段应保留: %+v", sink.pay[0])
	}
	if base["text"] != "a" {
		t.Fatalf("入参不应被就地改写: %+v", base)
	}
}

// TestChunkCoalescerFlushesOnShapeChange 形状变化（msg_id 变了）先落旧缓冲再开新缓冲：
// 跨消息的原始顺序不被重排（A1,A2,B1,A3 → "a1a2","b1","a3"）。
func TestChunkCoalescerFlushesOnShapeChange(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()

	add := func(msgID, text string) {
		c.Add(BusMessage, "s1", map[string]any{"session_id": "s1", "msg_id": msgID, "text": text})
	}
	add("m1", "a1")
	add("m1", "a2")
	add("m2", "b1")
	add("m1", "a3")
	c.FlushAll()
	want := []string{"a1a2", "b1", "a3"}
	if got := sink.texts(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("合并顺序不符: got %v want %v", got, want)
	}
}

// TestChunkCoalescerShapeChangeIsNotOnlyMsgID 除 text 外任何字段变化都不合并
// （否则会丢信息：如 {i:1} 与 {i:2} 合成 {i:1}）。
func TestChunkCoalescerShapeChangeIsNotOnlyMsgID(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()
	c.Add(BusMessage, "s-a", map[string]any{"i": 1})
	c.Add(BusMessage, "s-a", map[string]any{"i": 2})
	c.FlushAll()
	if got := sink.n(); got != 2 {
		t.Fatalf("形状不同应各成一条: %d", got)
	}
	if sink.pay[0]["i"] != 1 || sink.pay[1]["i"] != 2 {
		t.Fatalf("载荷被吞: %+v", sink.pay)
	}
}

// TestChunkCoalescerThresholds 阈值：单块达 4KB 或累计 64 条即同步落盘（不等窗口）。
func TestChunkCoalescerThresholds(t *testing.T) {
	// 字节阈值：一条 4KB 增量立即发布。
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	c.Add(BusMessage, "s1", map[string]any{"text": strings.Repeat("x", ChunkFlushBytes)})
	if got := sink.n(); got != 1 {
		t.Fatalf("达字节阈值应立即发布: %d", got)
	}
	c.Close()

	// 条数阈值：第 64 条触发，且后续能继续攒下一批。
	sink2 := &fakeSink{}
	c2 := NewCoalescer(sink2.fn)
	defer c2.Close()
	for i := 0; i < ChunkFlushCount; i++ {
		c2.Add(BusMessage, "s1", map[string]any{"msg_id": "m1", "text": "x"})
	}
	if got := sink2.n(); got != 1 {
		t.Fatalf("达条数阈值应立即发布: %d", got)
	}
	if got := sink2.texts()[0]; len(got) != ChunkFlushCount {
		t.Fatalf("合并文本长度不符: %d", len(got))
	}
	c2.Add(BusMessage, "s1", map[string]any{"msg_id": "m1", "text": "y"})
	if got := sink2.n(); got != 1 {
		t.Fatalf("阈值后应重新攒批: %d", got)
	}
}

// TestChunkCoalescerTimerFlush 定时窗口：无人 flush 时，25ms 窗口内自行落盘。
func TestChunkCoalescerTimerFlush(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()
	c.Add(BusMessage, "s1", map[string]any{"text": "later"})
	deadline := time.Now().Add(2 * time.Second)
	for sink.n() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sink.texts(); len(got) != 1 || got[0] != "later" {
		t.Fatalf("窗口到期应自动落盘: %v", got)
	}
}

// TestChunkCoalescerIsolatesTypesAndSessions 不同类型/不同会话各成一条（绝不混并）。
func TestChunkCoalescerIsolatesTypesAndSessions(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()
	c.Add(BusMessage, "s1", map[string]any{"text": "m"})
	c.Add(BusThought, "s1", map[string]any{"text": "t"})
	c.Add(BusMessage, "s2", map[string]any{"text": "o"})
	c.FlushAll()
	if got := sink.n(); got != 3 {
		t.Fatalf("应各成一条: %d %v", got, sink.texts())
	}
	if sink.typ[0] != BusMessage || sink.typ[1] != BusThought || sink.conv[2] != "s2" {
		t.Fatalf("类型/会话归属不符: %v %v", sink.typ, sink.conv)
	}
}

// TestChunkCoalescerCloseFlushes Close 落掉尾部增量（停机不丢最后一段文本）。
func TestChunkCoalescerCloseFlushes(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	c.Add(BusMessage, "s1", map[string]any{"text": "tail"})
	c.Close()
	c.Close() // 幂等
	if got := sink.texts(); len(got) != 1 || got[0] != "tail" {
		t.Fatalf("Close 应落掉尾部增量: %v", got)
	}
}

// TestChunkCoalescerFlushIsPerSession 非合并类型发布前的 flush 只落本会话
// （别的会话的增量继续攒批，不被无关事件反复打断）。
func TestChunkCoalescerFlushIsPerSession(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	defer c.Close()
	c.Add(BusMessage, "s1", map[string]any{"text": "a"})
	c.Add(BusMessage, "s2", map[string]any{"text": "b"})
	c.Flush("s1")
	if got := sink.texts(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("只应落 s1: %v", got)
	}
	c.Flush("s2")
	if got := sink.texts(); len(got) != 2 || got[1] != "b" {
		t.Fatalf("s2 应在其后落盘: %v", got)
	}
}

// TestChunkCoalescerNoGoroutineLeak Close 后定时窗口 goroutine 退出。
func TestChunkCoalescerNoGoroutineLeak(t *testing.T) {
	sink := &fakeSink{}
	c := NewCoalescer(sink.fn)
	c.Add(BusMessage, "s1", map[string]any{"text": "x"})
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未在 2s 内返回")
	}
	if got := sink.n(); got != 1 {
		t.Fatalf("Close 应落盘: %d", got)
	}
}
