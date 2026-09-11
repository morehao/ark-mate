package manager

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

// publish 把域事件发布进 eventbus（agg = conversationID；每 agg 版本递增），
// 并旁路追加到该会话的 wire transcript（未启用/非 map 载荷时跳过）。
// sqlite 模式下（st != nil）：事件先经 store.AppendEvent 在事务内取全局 seq，
// bus 以 PublishAt 镜像同一号序（跨重启连续）；DB 写失败时退化进程内取号并告警（不阻断会话）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (s *Service) publish(typ, conversationID string, payload any) eventbus.Event {
	s.mu.Lock()
	s.ver[conversationID]++
	v := s.ver[conversationID]
	s.mu.Unlock()

	bus := s.bus
	seq := int64(0)
	if st := s.st; st != nil {
		got, err := st.AppendEvent(context.Background(), conversationID, typ, payload)
		if err != nil {
			slog.Default().Warn("store: append event failed（退化为进程内取号）",
				"conversation", conversationID, "type", typ, "err", err)
		} else {
			seq = got
		}
	}
	var evt eventbus.Event
	if bus == nil {
		evt = eventbus.Event{Type: typ, AggregID: conversationID, Version: v, Seq: seq}
	} else if seq > 0 {
		evt = bus.PublishAt(seq, typ, conversationID, v, payload)
	} else {
		evt = bus.Publish(typ, conversationID, v, payload)
	}
	if m, ok := payload.(map[string]any); ok {
		s.publishSideEffects(conversationID, typ, m, evt.Seq)
	}
	// 终态事件 → runs 物化（sqlite 模式；窗口无新事件时幂等跳过）。
	if s.st != nil && terminalRunStatus(typ) != "" {
		if m, ok := payload.(map[string]any); ok {
			s.materializeRun(conversationID, typ, m)
		}
	}
	return evt
}

// publishSideEffects 非 map 载荷不触发的旁路副作用：
// wire transcript 追加 + 会话标题自动推导（首个用户消息；PATCH 可覆盖）。
func (s *Service) publishSideEffects(conversationID, typ string, m map[string]any, seq int64) {
	s.tx.appendLine(conversationID, seq, typ, m)
	// 会话标题自动推导：首个用户消息作为标题（截断）。PATCH 可覆盖（见 UpdateConversation）。
	// 须持 s.mu 写 Title（与 UpdateConversation/conversationResp 并发读写作同步，避免数据竞争）。
	if typ == BusUserMessage {
		var derived string
		s.mu.Lock()
		if r, ok := s.conversations[conversationID]; ok && strings.TrimSpace(r.Title) == "" {
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
			if err := s.st.UpdateConversationMeta(ctx, conversationID, store.ConversationMetaPatch{Title: &derived}); err != nil {
				slog.Default().Warn("store: derive title persist failed", "conversation", conversationID, "err", err)
			}
		}
	}
}
