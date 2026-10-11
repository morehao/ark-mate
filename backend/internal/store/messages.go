package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
)

// ——— messages 表（可重建读模型：轮内消息物化；events 仍为唯一事实源） ———

// MessageTextHit 一条命中（搜索片段与计数用）。
type MessageTextHit struct {
	SessionID string
	Role      string
	Text      string
}

// MessageHit 一条全局消息命中（跨会话搜索 + 锚点跳转用）。
type MessageHit struct {
	SessionID    string
	SessionTitle string
	AgentID      string
	Role         string
	MessageID    string
	RunID        string
	Seq          int64 // 锚点：消息起始事件序号（前端据此滚动定位）
	SeqHi        int64
	Text         string
	CreatedAt    time.Time
}

// CreateMessages 单事务批量插入一轮的 messages 行（可重复调用：同 run_id 先删后插，
// 保证「从 events 重建」幂等）。
func (s *Store) CreateMessages(ctx context.Context, msgs []model.MessageEntity) error {
	if len(msgs) == 0 {
		return nil
	}
	return s.messages.ReplaceRunMessages(ctx, msgs)
}

// SearchUserMessages 在指定会话的 user 消息中做子串检索（大小写不敏感；LIKE 通配转义），
// 返回全部命中行（service 侧折叠片段/计数）。
func (s *Store) SearchUserMessages(ctx context.Context, sessionID, q string) ([]MessageTextHit, error) {
	hits, err := s.messages.SearchUserText(ctx, sessionID, q)
	if err != nil {
		return nil, err
	}
	out := make([]MessageTextHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, MessageTextHit{SessionID: h.SessionID, Role: h.Role, Text: h.Text})
	}
	return out, nil
}

// ListMessages 列出某会话全部消息行（seq 序；测试与后续 REST 用）。
func (s *Store) ListMessages(ctx context.Context, sessionID string) ([]model.MessageEntity, error) {
	return s.messages.List(ctx, sessionID)
}

// SearchMessages 跨会话检索 messages 表正文（角色不限，大小写不敏感子串；LIKE 通配转义）。
// 排序：created_at 降序、seq 降序；截断 limit（调用方已校验 >0）。
func (s *Store) SearchMessages(ctx context.Context, q string, limit int) ([]MessageHit, error) {
	hits, err := s.messages.SearchText(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]MessageHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, MessageHit{
			SessionID:    h.SessionID,
			SessionTitle: h.SessionTitle,
			AgentID:      h.AgentID,
			Role:         h.Role,
			MessageID:    h.MessageID,
			RunID:        h.RunID,
			Seq:          h.Seq,
			SeqHi:        h.SeqHi,
			Text:         h.Text,
			CreatedAt:    h.CreatedAtTime(),
		})
	}
	return out, nil
}
