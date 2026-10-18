package store

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
)

// MessageHit 一条全局消息命中（形状在 `internal/model`；v3.1 P4 上移）。
type MessageHit = model.MessageHit

// ——— messages 表（可重建读模型：轮内消息物化；events 仍为唯一事实源） ———

// MessageTextHit 一条命中（搜索片段与计数用）。
type MessageTextHit = model.MessageTextHit

// ReplaceSessionMessages 单事务重铺某会话的 messages（先删会话全部旧行再插新行；
// 空批次 = 该会话清空）。这是**生产上的唯一写路径**（manager 的整会话重建）。
func (s *Store) ReplaceSessionMessages(ctx context.Context, sessionID string, msgs []model.MessageEntity) error {
	return s.messages.ReplaceSessionMessages(ctx, sessionID, msgs)
}

// CreateMessages 裸批量插入（**只给测试直插**）。生产的写路径只有 `ReplaceSessionMessages`；
// 这里刻意不做"先删后插"——直插的行要自己给 `ordinal`（`UNIQUE(session_id, ordinal)` 会咬重号）。
func (s *Store) CreateMessages(ctx context.Context, msgs []model.MessageEntity) error {
	return s.messages.InsertMessages(ctx, msgs)
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
			RuntimeID:    h.RuntimeID,
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
