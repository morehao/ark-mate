package manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/golib/glog"
)

// ——— messages 物化（run 收口，随 materializeRun 同窗口执行） ———
//
// 语义（读模型，从 events 可重建；doc §数据模型/表说明）：
//   - 一条消息行 = 一个可寻址文本片段：role=user 一行（输入原文）；role=assistant 为一段连续 prose
//     （BusMessage 增量按 msg_id/时序拼接；msg_id 缺失时同轮连续 prose 合并）。
//   - 非 prose 事件（thought/tool 交错）不单独成行，进入其归属 assistant 行的事件时间线
//     （events_json 保留 text/tool/thought 交错序），工具最终态抽到 tool_details_json（按 id 去重）。
//   - 同一 run 幂等重建：CreateMessages 先删后插（run_id 作用域）。

type msgRowBuilder struct {
	role      string
	msgID     string
	text      string
	seqLo     int64
	seqHi     int64
	events    []map[string]any
	tools     map[string]map[string]any // tool_call_id → 最终态（input/output 合并）
	createdAt time.Time
}

// materializeMessages 把一轮窗口事件物化为 messages 行（assistant 文本片段 + 时间线 + 工具最终态）。
// best-effort：失败仅告警，不影响 events 事实源。
func (s *Service) materializeMessages(sessionID, runID string, evs []model.EventEntity) {
	if s.st == nil || len(evs) == 0 {
		return
	}
	rows := buildMessageRows(sessionID, runID, evs)
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.CreateMessages(ctx, rows); err != nil {
		glog.Warnw(context.Background(), "store: create messages failed", "session", sessionID, "run", runID, "err", err)
	}
}

func buildMessageRows(sessionID, runID string, evs []model.EventEntity) []model.MessageEntity {
	var out []model.MessageEntity
	var cur *msgRowBuilder
	flush := func() {
		if cur == nil {
			return
		}
		td, err := json.Marshal(toolDetailList(cur.tools))
		if err != nil {
			td = []byte("[]")
		}
		ej, err := json.Marshal(cur.events)
		if err != nil {
			ej = []byte("[]")
		}
		at := cur.createdAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		out = append(out, model.MessageEntity{
			ID:        objid.New(objid.Message),
			SessionID: sessionID, RunID: runID, Role: cur.role, Text: cur.text,
			EventsJSON: string(ej), ToolDetailsJSON: string(td),
			SeqLo: cur.seqLo, SeqHi: cur.seqHi, CreatedAt: at,
		})
		cur = nil
	}

	for _, e := range evs {
		var m map[string]any
		if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err != nil {
			m = map[string]any{}
		}
		evObj := map[string]any{"seq": e.Seq, "type": e.Type, "payload": m}

		switch e.Type {
		case BusUserMessage:
			flush()
			text, _ := m["text"].(string)
			out = append(out, model.MessageEntity{
				ID:        objid.New(objid.Message),
				SessionID: sessionID, RunID: runID, Role: "user", Text: text,
				EventsJSON: mustJSON([]map[string]any{evObj}),
				SeqLo:      e.Seq, SeqHi: e.Seq, CreatedAt: e.CreatedAt,
			})
		case BusMessage:
			text, _ := m["text"].(string)
			msgID, _ := m["msg_id"].(string)
			if cur == nil || (msgID != "" && cur.msgID != "" && msgID != cur.msgID) {
				flush()
				cur = &msgRowBuilder{role: "assistant", msgID: msgID, createdAt: e.CreatedAt}
			} else if cur != nil && cur.msgID == "" && msgID != "" && len(cur.events) > 0 {
				// 无 msg_id 首段后带出 msg_id → 归属同一 assistant 消息（SDK 补 id 场景）
				cur.msgID = msgID
			}
			if cur.seqLo == 0 {
				cur.seqLo = e.Seq
			}
			cur.seqHi = e.Seq
			cur.text += text
			cur.events = append(cur.events, evObj)
		default:
			// thought/tool/usage/terminal：归属当前 assistant 行（无则开一个空文本行承载时间线）。
			if cur == nil {
				cur = &msgRowBuilder{role: "assistant", createdAt: e.CreatedAt}
			}
			if cur.seqLo == 0 {
				cur.seqLo = e.Seq
			}
			cur.seqHi = e.Seq
			cur.events = append(cur.events, evObj)
			if cur.tools == nil {
				cur.tools = map[string]map[string]any{}
			}
			toolDetailsFromEvent(cur.tools, e.Type, m)
		}
	}
	flush()
	return out
}

// toolDetailsFromEvent 把工具事件合入 tool_call_id → 最终态（BusToolCall 记 input，BusToolUpdate 记 output）。
func toolDetailsFromEvent(tools map[string]map[string]any, typ string, m map[string]any) {
	id, _ := m["tool_call_id"].(string)
	if id == "" {
		return
	}
	st, ok := tools[id]
	if !ok {
		st = map[string]any{"tool_call_id": id}
		if k, _ := m["kind"].(string); k != "" {
			st["kind"] = k
		}
		tools[id] = st
	}
	switch typ {
	case BusToolCall:
		if in, ok := m["input"].(string); ok {
			st["input"] = in
		}
	case BusToolUpdate:
		if out, ok := m["output"].(string); ok {
			st["output"] = out
		}
		if ok, _ := m["ok"].(bool); ok {
			st["ok"] = true
		}
	}
}

func toolDetailList(tools map[string]map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, st := range tools {
		out = append(out, st)
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}
