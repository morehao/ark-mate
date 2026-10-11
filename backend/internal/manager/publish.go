package manager

import (
	"context"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/golib/glog"
)

// publish 是**事件的唯一出口**，分两段：
//
//  1. ③ 增量合并：文本增量（session.message/thought）进 chunkCoalescer 攒批，其余类型在此
//     **先 flush 本会话缓冲再发布**——保证「文本先于终态/工具卡」的 seq 顺序不被越过；
//  2. publishRaw：脱敏闸门 + 落 events 表（事务内取全局 seq）+ 镜像进 bus + transcript 旁路。
//
// 缓冲增量的真正发布走同一 publishRaw（由定时窗口/阈值/终端事件触发），故落库与推流路径唯一。
// 合并不改变事件 type 与 payload 形状（仍带 session_id/agent_id/msg_id 与拼接后的 text），
// 前端按 msg_id 追加同一条消息的渲染逻辑无需改动。
func (s *Service) publish(typ, sessionID string, payload any) eventbus.Event {
	if c := s.coalescer; c != nil {
		if m, ok := payload.(map[string]any); ok && c.offers(typ) {
			c.add(typ, sessionID, m)
			return eventbus.Event{}
		}
		c.flush(sessionID)
	}
	return s.publishRaw(typ, sessionID, payload)
}

// flushChunks 落掉全部缓冲增量（停机/关闭 transcript 前调用，避免丢尾部文本）。
func (s *Service) flushChunks() {
	s.coalescer.flushAll()
}

// publishRaw 把域事件发布进 eventbus（agg = sessionID；每 agg 版本递增），
// 并旁路追加到该会话的 wire transcript（未启用/非 map 载荷时跳过）。
// sqlite 模式下（st != nil）：事件先经 store.AppendEvent 在事务内取全局 seq，
// bus 以 PublishAt 镜像同一号序（跨重启连续）；DB 写失败时退化进程内取号并告警（不阻断会话）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (s *Service) publishRaw(typ, sessionID string, payload any) eventbus.Event {
	// 脱敏闸门（唯一入口）：事件在此之后才落 events 表 / 进 transcript / 推 WS / 物化 runs，
	// 故一处过闸即覆盖全部下游（含历史回放读的是同一份已脱敏载荷）。
	payload = redact.Payload(payload)
	s.mu.Lock()
	s.ver[sessionID]++
	v := s.ver[sessionID]
	s.mu.Unlock()

	bus := s.bus
	seq := int64(0)
	if st := s.st; st != nil {
		got, err := st.AppendEvent(context.Background(), sessionID, typ, payload)
		if err != nil {
			glog.Warnw(context.Background(), "store: append event failed（退化为进程内取号）",
				"session", sessionID, "type", typ, "err", err)
		} else {
			seq = got
		}
	}
	var evt eventbus.Event
	if bus == nil {
		evt = eventbus.Event{Type: typ, AggregID: sessionID, Version: v, Seq: seq}
	} else if seq > 0 {
		evt = bus.PublishAt(seq, typ, sessionID, v, payload)
	} else {
		evt = bus.Publish(typ, sessionID, v, payload)
	}
	if m, ok := payload.(map[string]any); ok {
		s.publishSideEffects(sessionID, typ, m, evt.Seq)
	}
	// 终态事件 → runs 物化（sqlite 模式；窗口无新事件时幂等跳过）。
	if s.st != nil && terminalRunStatus(typ) != "" {
		if m, ok := payload.(map[string]any); ok {
			s.materializeRun(sessionID, typ, m)
		}
	}
	return evt
}

// publishSideEffects 非 map 载荷不触发的旁路副作用：
// wire transcript 追加 + 会话标题自动推导（首个用户消息；PATCH 可覆盖）。
func (s *Service) publishSideEffects(sessionID, typ string, m map[string]any, seq int64) {
	s.tx.appendLine(sessionID, seq, typ, m)
	// 会话标题自动推导：首个用户消息作为标题（截断）。PATCH 可覆盖（见 UpdateSession）。
	// 须持 s.mu 写 Title（与 UpdateSession/sessionResp 并发读写作同步，避免数据竞争）。
	if typ == BusUserMessage {
		var derived string
		s.mu.Lock()
		if r, ok := s.sessions[sessionID]; ok && strings.TrimSpace(r.Title) == "" {
			if text, _ := m["text"].(string); text != "" {
				derived = deriveTitle(text)
				r.Title = derived
				r.UpdatedAt = time.Now().UTC()
			}
		}
		s.mu.Unlock()
		if derived != "" && s.st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.st.UpdateSessionMeta(ctx, sessionID, model.SessionMetaPatch{Title: &derived}); err != nil {
				glog.Warnw(context.Background(), "store: derive title persist failed", "session", sessionID, "err", err)
			}
		}
	}
}
