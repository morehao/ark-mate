package run

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"
)

// 本文件是**合并器与真实出口的集成**用例（从 `chunk_coalescer_test.go` 拆出）。
//
// 为什么它留在 manager 而不是随合并器进域：这三条要一套完整 rig（真 Service + 真库 + 真
// transcript），验的是"合并器在 publish/closeTranscripts 这条线上真的被执行到"——域内的
// 纯用例（攒批/形状变化/阈值/定时/隔离）不依赖这些接线，两边不互相替代。

// TestPublishCoalescesDeltasAheadOfToolCall 走真实 publish 出口：文本增量攒批，
// 工具卡（非合并类型）发布前先落文本 → events 表里「文本先于工具卡」，且文本已合并。
func TestPublishCoalescesDeltasAheadOfToolCall(t *testing.T) {
	svc, st := newSearchRig(t)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "cv-merge"})

	svc.sessionPlane.Publish(session.BusMessage, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "m1", "text": "你"})
	svc.sessionPlane.Publish(session.BusMessage, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "m1", "text": "好"})
	svc.sessionPlane.Publish(session.BusToolCall, "cv-merge", map[string]any{"session_id": "cv-merge", "tool": "read"})
	svc.sessionPlane.Publish(session.BusThought, "cv-merge", map[string]any{"session_id": "cv-merge", "msg_id": "t1", "text": "想"})

	rows, _, err := st.EventsAfter(context.Background(), "cv-merge", -1, 10)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Type)
	}
	want := []string{session.BusMessage, session.BusToolCall}
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
	svc.sessionPlane.FlushChunks()
	rows2, _, _ := st.EventsAfter(context.Background(), "cv-merge", -1, 10)
	if len(rows2) != 3 || rows2[2].Type != session.BusThought {
		t.Fatalf("尾部落盘不符: %+v", rows2)
	}
}

// TestPublishFlushesDeltasOnRunEnded 终态事件前必 flush：events 表里文本先于 run.ended。
func TestPublishFlushesDeltasOnRunEnded(t *testing.T) {
	svc, st := newSearchRig(t)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "cv-end"})
	for _, chunk := range []string{"最", "后", "一段"} {
		svc.sessionPlane.Publish(session.BusMessage, "cv-end", map[string]any{"session_id": "cv-end", "msg_id": "m1", "text": chunk})
	}
	svc.sessionPlane.Publish(session.BusRunEnded, "cv-end", map[string]any{"session_id": "cv-end", "stop_reason": "end_turn"})

	rows, _, err := st.EventsAfter(context.Background(), "cv-end", -1, 10)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(rows) != 2 || rows[0].Type != session.BusMessage || rows[1].Type != session.BusRunEnded {
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
	svc.SessionPlane().SetTranscriptDir(dir)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "cv-close"})
	transcriptPlane(svc).BeginTranscript("cv-close", "stub", "/tmp", false)
	svc.sessionPlane.Publish(session.BusMessage, "cv-close", map[string]any{"session_id": "cv-close", "msg_id": "m1", "text": "尾巴"})

	svc.sessionPlane.CloseTranscripts()
	rows, _, err := st.EventsAfter(context.Background(), "cv-close", -1, 10)
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
