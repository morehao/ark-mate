package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/golib/glog"
)

// SSEHandler 是 SSE 的协议薄适配层：订阅事件总线，把可重放/增量事件
// 逐一转成 `text/event-stream` 帧，并叠加连接/心跳编排帧。
// 业务事件源（真实 ACP notification）由 manager 注入总线，见事件总线范式 §2。
type SSEHandler struct {
	bus *eventbus.Bus
}

// heartbeatInterval 心跳发送周期。
const heartbeatInterval = 3 * time.Second

// Stream 推送统一会话事件流。after 来源：query `after` 或 SSE 标准 Last-Event-ID。
func (h *SSEHandler) Stream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	after := seqFrom(c)
	sub := h.bus.Subscribe(after)
	defer h.bus.Unsubscribe(sub)

	// 编排帧：连接建立
	writeFrame(c.Writer, "server", 0, fmt.Sprintf(`{"type":"server.connected","seq":0,"after":%d}`, after))

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case evt := <-sub.C:
			writeEvent(w, evt)
			return true
		case <-ticker.C:
			writeHeartbeat(w, h.bus.LastSeq())
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})

	glog.Info(c.Request.Context(), "sse: stream closed", "after", after)
}

// seqFrom 从请求解析续传游标：优先 query `after`，否则用 Last-Event-ID。
func seqFrom(c *gin.Context) int64 {
	if raw := c.Query("after"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	}
	if lid := c.GetHeader("Last-Event-ID"); lid != "" {
		if n, err := strconv.ParseInt(lid, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// writeEvent 将一个总线事件写成 SSE 帧，id 用 seq 支持 Last-Event-ID 续传。
func writeEvent(w io.Writer, evt eventbus.Event) {
	pl, _ := json.Marshal(struct {
		Type     string `json:"type"`
		Seq      int64  `json:"seq"`
		AggregID string `json:"agg_id"`
		Version  int64  `json:"version"`
		Payload  any    `json:"payload,omitempty"`
	}{evt.Type, evt.Seq, evt.AggregID, evt.Version, evt.Payload})
	writeFrame(w, evt.Type, evt.Seq, string(pl))
}

// writeHeartbeat 写入一条 server.heartbeat 编排帧（id 标注当前监听位置）。
func writeHeartbeat(w io.Writer, seq int64) {
	writeFrame(w, "server", seq, `{"type":"server.heartbeat"}`)
}

// writeFrame 写一条标准 SSE 帧并 Flush。
func writeFrame(w io.Writer, event string, id int64, data string) {
	if id == 0 {
		fmt.Fprintf(w, "event: %s\n", event)
	} else {
		fmt.Fprintf(w, "id: %d\nevent: %s\n", id, event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(interface{ Flush() }); ok {
		f.Flush()
	}
}
