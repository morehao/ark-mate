package handler_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

// TestWSHandlerAttachSnapshotLivePong 覆盖 §4.5 主路径：
// attach（无 since → snapshot）→ bus 发布 → event 帧 → ping → pong → detach。
func TestWSHandlerAttachSnapshotLivePong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bus := eventbus.New()
	h := handler.NewWSHandler(bus)

	eng := gin.New()
	eng.GET("/ws/events", h.Handle)
	srv := httptest.NewServer(eng)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// attach 无 since → snapshot
	if err := conn.WriteJSON(map[string]any{"action": "attach", "agg_id": "s1"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	var snap frame
	if err := conn.ReadJSON(&snap); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap.Type != "snapshot" || snap.AggID != "s1" {
		t.Fatalf("snapshot 帧不符: %+v", snap)
	}

	// bus 发布 → event 帧（按聚合过滤）
	pub := bus.Publish("session.message", "s1", 1, map[string]any{"text": "hi"})
	var evt frame
	if err := conn.ReadJSON(&evt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if evt.Type != "event" || evt.AggID != "s1" || evt.Event == nil ||
		evt.Event.Type != "session.message" || evt.Event.Seq != pub.Seq {
		t.Fatalf("event 帧不符: %+v", evt)
	}

	// 未 attach 的聚合不推送（只影响同连接；此处另发一个事件验证过滤）
	bus.Publish("session.thought", "s2", 1, map[string]any{"text": "other"})
	// ping → pong
	if err := conn.WriteJSON(map[string]any{"action": "ping"}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	var pong frame
	if err := conn.ReadJSON(&pong); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if pong.Type != "pong" {
		t.Fatalf("期望 pong，got %+v", pong)
	}

	// detach → detached
	if err := conn.WriteJSON(map[string]any{"action": "detach", "agg_id": "s1"}); err != nil {
		t.Fatalf("detach: %v", err)
	}
	var det frame
	if err := conn.ReadJSON(&det); err != nil {
		t.Fatalf("read detached: %v", err)
	}
	if det.Type != "detached" || det.AggID != "s1" {
		t.Fatalf("detached 帧不符: %+v", det)
	}
}

// TestWSHandlerReplayAfterSince 验证带 since_seq 的 attach 走历史重放。
func TestWSHandlerReplayAfterSince(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bus := eventbus.New()
	e1 := bus.Publish("session.message", "s1", 1, map[string]any{"text": "one"}) // seq 1
	e2 := bus.Publish("session.message", "s1", 1, map[string]any{"text": "two"}) // seq 2
	bus.Publish("session.message", "s2", 1, map[string]any{"text": "other"})     // seq 3

	h := handler.NewWSHandler(bus)
	eng := gin.New()
	eng.GET("/ws/events", h.Handle)
	srv := httptest.NewServer(eng)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// attach s1 since=1 → 重放 seq2（仅该聚合）
	since := int64(e1.Seq)
	if err := conn.WriteJSON(map[string]any{"action": "attach", "agg_id": "s1", "since_seq": since}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	var replay frame
	if err := conn.ReadJSON(&replay); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if replay.Type != "event" || replay.AggID != "s1" || replay.Event == nil || replay.Event.Seq != e2.Seq {
		t.Fatalf("重放帧不符: %+v", replay)
	}
}
