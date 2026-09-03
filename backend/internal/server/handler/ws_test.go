package handler_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// frame 是测试用 WS 帧（松散解析关键字段）。
type frame struct {
	Type  string          `json:"type"`
	AggID string          `json:"agg_id"`
	Seq   int64           `json:"seq"`
	State json.RawMessage `json:"state,omitempty"`
	Event *struct {
		Type    string         `json:"type"`
		Seq     int64          `json:"seq"`
		AggID   string         `json:"agg_id"`
		Payload map[string]any `json:"payload"`
	} `json:"event,omitempty"`
}

// startWS 起测试服务并拨号（coder/websocket），返回连接与上下文辅助。
func startWS(t *testing.T, bus *eventbus.Bus) *websocket.Conn {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handler.NewWSHandler(bus)
	eng := gin.New()
	eng.GET("/ws/events", h.Handle)
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// wsWrite 以文本帧发送 JSON。
func wsWrite(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// wsRead 读下一文本帧并解 JSON。
func wsRead(t *testing.T, c *websocket.Conn, dst any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("期望 text 帧，got type %v", typ)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// TestWSHandlerAttachSnapshotLivePong 覆盖 §4.5 主路径：
// attach（无 since → snapshot）→ bus 发布 → event 帧 → ping → pong → detach。
func TestWSHandlerAttachSnapshotLivePong(t *testing.T) {
	bus := eventbus.New()
	conn := startWS(t, bus)

	// attach 无 since → snapshot
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})
	var snap frame
	wsRead(t, conn, &snap)
	if snap.Type != "snapshot" || snap.AggID != "s1" {
		t.Fatalf("snapshot 帧不符: %+v", snap)
	}

	// bus 发布 → event 帧（按聚合过滤）
	pub := bus.Publish("session.message", "s1", 1, map[string]any{"text": "hi"})
	var evt frame
	wsRead(t, conn, &evt)
	if evt.Type != "event" || evt.AggID != "s1" || evt.Event == nil ||
		evt.Event.Type != "session.message" || evt.Event.Seq != pub.Seq {
		t.Fatalf("event 帧不符: %+v", evt)
	}

	// 未 attach 的聚合不推送（此处不发帧验证：发布给 s2 不会产生本连接可见帧，
	// 直接进入 ping 流程，避免盲等；过滤逻辑由 replay 测试的仅 s1 回放间接覆盖）
	bus.Publish("session.thought", "s2", 1, map[string]any{"text": "other"})

	// ping → pong
	wsWrite(t, conn, map[string]any{"action": "ping"})
	var pong frame
	wsRead(t, conn, &pong)
	if pong.Type != "pong" {
		t.Fatalf("期望 pong，got %+v", pong)
	}

	// detach → detached
	wsWrite(t, conn, map[string]any{"action": "detach", "agg_id": "s1"})
	var det frame
	wsRead(t, conn, &det)
	if det.Type != "detached" || det.AggID != "s1" {
		t.Fatalf("detached 帧不符: %+v", det)
	}
}

// TestWSHandlerReplayAfterSince 验证带 since_seq 的 attach 走历史重放（仅该聚合）。
func TestWSHandlerReplayAfterSince(t *testing.T) {
	bus := eventbus.New()
	e1 := bus.Publish("session.message", "s1", 1, map[string]any{"text": "one"}) // seq 1
	e2 := bus.Publish("session.message", "s1", 1, map[string]any{"text": "two"}) // seq 2
	bus.Publish("session.message", "s2", 1, map[string]any{"text": "other"})     // seq 3

	conn := startWS(t, bus)

	// attach s1 since=1 → 重放 seq2（仅该聚合）
	since := e1.Seq
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": since})
	var replay frame
	wsRead(t, conn, &replay)
	if replay.Type != "event" || replay.AggID != "s1" || replay.Event == nil || replay.Event.Seq != e2.Seq {
		t.Fatalf("重放帧不符: %+v", replay)
	}
}

// TestWSHandlerFullReplaySinceZero 验证 since_seq=0 → 全量回放（UI 冷开启既有会话重建视图）。
func TestWSHandlerFullReplaySinceZero(t *testing.T) {
	bus := eventbus.New()
	bus.Publish("session.message", "s1", 1, map[string]any{"text": "one"}) // seq 1
	bus.Publish("session.thought", "s1", 1, map[string]any{"text": "t"})   // seq 2
	bus.Publish("session.message", "s2", 1, map[string]any{"text": "no"})  // seq 3

	conn := startWS(t, bus)

	since := int64(0)
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": since})
	var seqs []int64
	for len(seqs) < 2 {
		var f frame
		wsRead(t, conn, &f)
		if f.Type != "event" || f.AggID != "s1" || f.Event == nil {
			t.Fatalf("期望 s1 的事件帧，got %+v", f)
		}
		seqs = append(seqs, f.Event.Seq)
	}
	if seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("全量回放序号不符: %v", seqs)
	}
}
