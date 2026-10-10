package manager

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSink 收集合并后的发布（保序），用于直接驱动 chunkCoalescer。
type fakeSink struct {
	mu   sync.Mutex
	typ  []string
	conv []string
	pay  []map[string]any
}

func (f *fakeSink) fn(typ, conversationID string, payload any) {
	m, _ := payload.(map[string]any)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typ = append(f.typ, typ)
	f.conv = append(f.conv, conversationID)
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
	c := newChunkCoalescer(sink.fn)
	defer c.Close()

	base := map[string]any{"session_id": "s1", "agent_id": "a1", "msg_id": "m1", "text": "a"}
	c.add(BusMessage, "s1", base)
	c.add(BusMessage, "s1", map[string]any{"session_id": "s1", "agent_id": "a1", "msg_id": "m1", "text": "b"})
	c.add(BusMessage, "s1", map[string]any{"session_id": "s1", "agent_id": "a1", "msg_id": "m1", "text": "c"})
	if got := sink.n(); got != 0 {
		t.Fatalf("未达阈值不应发布: %d", got)
	}
	c.flushAll()
	if got := sink.texts(); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("应合并成一条 abc: %v", got)
	}
	if sink.typ[0] != BusMessage || sink.conv[0] != "s1" {
		t.Fatalf("类型/会话不符: %s %s", sink.typ[0], sink.conv[0])
	}
	if sink.pay[0]["msg_id"] != "m1" || sink.pay[0]["agent_id"] != "a1" {
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
	c := newChunkCoalescer(sink.fn)
	defer c.Close()

	add := func(msgID, text string) {
		c.add(BusMessage, "s1", map[string]any{"session_id": "s1", "msg_id": msgID, "text": text})
	}
	add("m1", "a1")
	add("m1", "a2")
	add("m2", "b1")
	add("m1", "a3")
	c.flushAll()
	want := []string{"a1a2", "b1", "a3"}
	if got := sink.texts(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("合并顺序不符: got %v want %v", got, want)
	}
}

// TestChunkCoalescerShapeChangeIsNotOnlyMsgID 除 text 外任何字段变化都不合并
// （否则会丢信息：如 {i:1} 与 {i:2} 合成 {i:1}）。
func TestChunkCoalescerShapeChangeIsNotOnlyMsgID(t *testing.T) {
	sink := &fakeSink{}
	c := newChunkCoalescer(sink.fn)
	defer c.Close()
	c.add(BusMessage, "s-a", map[string]any{"i": 1})
	c.add(BusMessage, "s-a", map[string]any{"i": 2})
	c.flushAll()
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
	c := newChunkCoalescer(sink.fn)
	c.add(BusMessage, "s1", map[string]any{"text": strings.Repeat("x", chunkFlushBytes)})
	if got := sink.n(); got != 1 {
		t.Fatalf("达字节阈值应立即发布: %d", got)
	}
	c.Close()

	// 条数阈值：第 64 条触发，且后续能继续攒下一批。
	sink2 := &fakeSink{}
	c2 := newChunkCoalescer(sink2.fn)
	defer c2.Close()
	for i := 0; i < chunkFlushCount; i++ {
		c2.add(BusMessage, "s1", map[string]any{"msg_id": "m1", "text": "x"})
	}
	if got := sink2.n(); got != 1 {
		t.Fatalf("达条数阈值应立即发布: %d", got)
	}
	if got := sink2.texts()[0]; len(got) != chunkFlushCount {
		t.Fatalf("合并文本长度不符: %d", len(got))
	}
	c2.add(BusMessage, "s1", map[string]any{"msg_id": "m1", "text": "y"})
	if got := sink2.n(); got != 1 {
		t.Fatalf("阈值后应重新攒批: %d", got)
	}
}

// TestChunkCoalescerTimerFlush 定时窗口：无人 flush 时，25ms 窗口内自行落盘。
func TestChunkCoalescerTimerFlush(t *testing.T) {
	sink := &fakeSink{}
	c := newChunkCoalescer(sink.fn)
	defer c.Close()
	c.add(BusMessage, "s1", map[string]any{"text": "later"})
	deadline := time.Now().Add(2 * time.Second)
	for sink.n() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sink.texts(); len(got) != 1 || got[0] != "later" {
		t.Fatalf("窗口到期应自动落盘: %v", got)
	}
}

// TestChunkCoalescerIsolatesTypesAndConversations 不同类型/不同会话各成一条（绝不混并）。
func TestChunkCoalescerIsolatesTypesAndConversations(t *testing.T) {
	sink := &fakeSink{}
	c := newChunkCoalescer(sink.fn)
	defer c.Close()
	c.add(BusMessage, "s1", map[string]any{"text": "m"})
	c.add(BusThought, "s1", map[string]any{"text": "t"})
	c.add(BusMessage, "s2", map[string]any{"text": "o"})
	c.flushAll()
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
	c := newChunkCoalescer(sink.fn)
	c.add(BusMessage, "s1", map[string]any{"text": "tail"})
	c.Close()
	c.Close() // 幂等
	if got := sink.texts(); len(got) != 1 || got[0] != "tail" {
		t.Fatalf("Close 应落掉尾部增量: %v", got)
	}
}

// TestChunkCoalescerFlushIsPerConversation 非合并类型发布前的 flush 只落本会话
// （别的会话的增量继续攒批，不被无关事件反复打断）。
func TestChunkCoalescerFlushIsPerConversation(t *testing.T) {
	sink := &fakeSink{}
	c := newChunkCoalescer(sink.fn)
	defer c.Close()
	c.add(BusMessage, "s1", map[string]any{"text": "a"})
	c.add(BusMessage, "s2", map[string]any{"text": "b"})
	c.flush("s1")
	if got := sink.texts(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("只应落 s1: %v", got)
	}
	c.flush("s2")
	if got := sink.texts(); len(got) != 2 || got[1] != "b" {
		t.Fatalf("s2 应在其后落盘: %v", got)
	}
}

// TestPublishCoalescesDeltasAheadOfToolCall 走真实 publish 出口：文本增量攒批，
// 工具卡（非合并类型）发布前先落文本 → events 表里「文本先于工具卡」，且文本已合并。
func TestPublishCoalescesDeltasAheadOfToolCall(t *testing.T) {
	svc, st := newSearchRig(t)
	svc.registerConversation(&ConversationRec{ID: "cv-merge", AgentID: "a1"})

	svc.publish(BusMessage, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "m1", "text": "你"})
	svc.publish(BusMessage, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "m1", "text": "好"})
	svc.publish(BusToolCall, "cv-merge", map[string]any{"session_id": "cv-merge", "tool": "read"})
	svc.publish(BusThought, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "t1", "text": "想"})

	rows, _, err := st.EventsAfter(context.Background(), "cv-merge", 0, 10)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Type)
	}
	want := []string{BusMessage, BusToolCall}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("事件类型/顺序不符: %v want %v", got, want)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &m); err != nil {
		t.Fatalf("载荷解析: %v", err)
	}
	if m["text"] != "你好" {
		t.Fatalf("文本应合并为「你好」: %+v", m)
	}
	// thought 仍在缓冲里（工具卡只 flush 了同会话的全部缓冲？——是的：flush 按会话，
	// 故 thought 也被落下，只是它在工具卡之后入缓冲，顺序为 文本→工具卡→thought）。
	svc.flushChunks()
	rows2, _, _ := st.EventsAfter(context.Background(), "cv-merge", 0, 10)
	if len(rows2) != 3 || rows2[2].Type != BusThought {
		t.Fatalf("尾部落盘不符: %+v", rows2)
	}
}

// TestPublishFlushesDeltasOnTurnEnded 终态事件前必 flush：events 表里文本先于 turn.ended。
func TestPublishFlushesDeltasOnTurnEnded(t *testing.T) {
	svc, st := newSearchRig(t)
	svc.registerConversation(&ConversationRec{ID: "cv-end", AgentID: "a1"})
	for _, chunk := range []string{"最", "后", "一段"} {
		svc.publish(BusMessage, "cv-end", map[string]any{"session_id": "cv-end", "msg_id": "m1", "text": chunk})
	}
	svc.publish(BusTurnEnded, "cv-end", map[string]any{"session_id": "cv-end", "stop_reason": "end_turn"})

	rows, _, err := st.EventsAfter(context.Background(), "cv-end", 0, 10)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(rows) != 2 || rows[0].Type != BusMessage || rows[1].Type != BusTurnEnded {
		t.Fatalf("终态前应落文本: %+v", rows)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(rows[0].PayloadJSON), &m)
	if m["text"] != "最后一段" {
		t.Fatalf("终态前文本应完整: %+v", m)
	}
	if rows[1].Seq != rows[0].Seq+1 {
		t.Fatalf("号序应连续: %d %d", rows[0].Seq, rows[1].Seq)
	}
}

// TestCloseTranscriptsFlushesChunks 停机路径（closeTranscripts）不丢尾部文本。
func TestCloseTranscriptsFlushesChunks(t *testing.T) {
	svc, st := newSearchRig(t)
	dir := t.TempDir()
	svc.SetTranscriptDir(dir)
	svc.registerConversation(&ConversationRec{ID: "cv-close", AgentID: "a1"})
	svc.tx.begin("cv-close", "a1", "stub", "/tmp", false)
	svc.publish(BusMessage, "cv-close", map[string]any{"session_id": "cv-close", "msg_id": "m1", "text": "尾巴"})

	svc.closeTranscripts()
	rows, _, err := st.EventsAfter(context.Background(), "cv-close", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("停机应落掉缓冲: n=%d err=%v", len(rows), err)
	}
	raw, err := os.ReadFile(dir + "/cv-close.jsonl")
	if err != nil {
		t.Fatalf("读 transcript: %v", err)
	}
	if !strings.Contains(string(raw), "尾巴") {
		t.Fatalf("transcript 应含尾部文本: %s", raw)
	}
}

// TestChunkCoalescerNoGoroutineLeak Close 后定时窗口 goroutine 退出。
func TestChunkCoalescerNoGoroutineLeak(t *testing.T) {
	sink := &fakeSink{}
	c := newChunkCoalescer(sink.fn)
	c.add(BusMessage, "s1", map[string]any{"text": "x"})
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
